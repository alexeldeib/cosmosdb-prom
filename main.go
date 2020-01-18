package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"

	"github.com/a8m/documentdb"
	"github.com/gogo/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/motemen/go-loghttp"
	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/prompb"
	flag "github.com/spf13/pflag"
	"github.com/google/uuid"
)

type timeseries struct {
	documentdb.Document
	Labels  []*prompb.Label `json:"labels"`
	Samples []prompb.Sample `json:"samples"`
}

type server struct {
	dbclient *documentdb.DocumentDB
	db       *documentdb.Database
	coll     *documentdb.Collection
}

func newServer(url, key, dbName, collection string) (*server, error) {
	if url == "" {
		return nil, errors.New("url must be non-empty")
	}

	if key == "" {
		return nil, errors.New("key must be non-empty")
	}

	client := documentdb.New(url, &documentdb.Config{
		MasterKey: &documentdb.Key{
			Key: key,
		},
		Client: http.Client{
			Transport: &loghttp.Transport{
				LogRequest: func(req *http.Request) {
					body, _ := ioutil.ReadAll(req.Body)
					fmt.Printf("%s", string(body))
					req.Body = ioutil.NopCloser(bytes.NewReader(body))
				},
			},
		},
	})

	dbs, err := client.QueryDatabases(documentdb.NewQuery(fmt.Sprintf("SELECT * FROM ROOT r WHERE r.id='%s'", dbName)))
	if err != nil {
		return nil, errors.Wrap(err, "failed to query dbs")
	}

	var db *documentdb.Database
	if len(dbs) == 0 {
		db, err = client.CreateDatabase(fmt.Sprintf(`{ "id": "%s" }`, dbName))
		if err != nil {
			return nil, errors.Wrap(err, "failed to create db")
		}
	} else {
		db = &dbs[0]
	}

	colls, err := client.QueryCollections(db.Self, documentdb.NewQuery(fmt.Sprintf("SELECT * FROM ROOT r WHERE r.id='%s'", collection)))
	if err != nil {
		return nil, errors.Wrap(err, "failed to query dbs")
	}

	var coll *documentdb.Collection
	if len(colls) == 0 {
		coll, err = client.CreateCollection(db.Self, fmt.Sprintf(`{ "id": "%s" }`, collection))
		if err != nil {
			return nil, errors.Wrap(err, "failed to create db")
		}
	} else {
		coll = &colls[0]
	}

	return &server{
		dbclient: client,
		db:       db,
		coll:     coll,
	}, nil
}

func (s *server) receive(w http.ResponseWriter, r *http.Request) {
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	reqBuf, err := snappy.Decode(nil, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var req prompb.WriteRequest
	if err = proto.Unmarshal(reqBuf, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	id, err := uuid.NewRandom()
	if err != nil {
		log.Printf("failed to create uuid: %v\n", err)
	}

	for _, ts := range req.Timeseries {
		if _, err := s.dbclient.UpsertDocument(s.coll.Self, timeseries{
			Document: documentdb.Document{
				Resource: documentdb.Resource{
					Id: id.String(),
				},
			},
			Labels:  ts.Labels,
			Samples: ts.Samples,
		}); err != nil {
			log.Fatalf("failed to upsert timeseries: %v", err)
		}
	}
}

func main() {
	var key string
	var url string
	var dbName string
	var collection string

	flag.StringVar(&key, "key", "", "master key to cosmosdb account")
	flag.StringVar(&url, "url", "", "connection url to cosmosdb")
	flag.StringVar(&dbName, "db", "prometheus", "db to use")
	flag.StringVar(&collection, "collection", "metrics", "collection name in database (or container)")

	if key == "" {
		key = os.Getenv("COSMOSDB_KEY")
	}

	if url == "" {
		url = os.Getenv("COSMOSDB_URL")
	}

	server, err := newServer(url, key, dbName, collection)
	if err != nil {
		log.Fatalf("failed to initialize server: %v", err)
	}

	http.HandleFunc("/receive", server.receive)

	log.Fatal(http.ListenAndServe(":1234", nil))
}
