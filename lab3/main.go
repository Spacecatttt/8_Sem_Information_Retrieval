package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"
)

type Game struct {
	ID          string   `json:"id,omitempty"`
	Title       string   `json:"title"`
	Developer   string   `json:"developer"`
	Genre       []string `json:"genre"`
	ReleaseYear int      `json:"release_year"`
	Rating      float64  `json:"rating"`
}

var elasticUrl string
var elasticUsername string
var elasticPassword string

func main() {
	// load .env file
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
		return
	}

	elasticUrl = os.Getenv("ELASTIC_URL")
	elasticUsername = os.Getenv("ELASTIC_USERNAME")
	elasticPassword = os.Getenv("ELASTIC_PASSWORD")

	initIndex()

	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/api/games/add", addHandler)
	http.HandleFunc("/api/games/update", updateHandler)
	http.HandleFunc("/api/games/search", searchHandler)
	http.HandleFunc("/api/games/delete", deleteHandler)

	fmt.Println("Server started at http://localhost:8080")
	http.ListenAndServe(":8080", nil)
}

type ElasticResponse struct {
	StatusCode int
	Data       []byte
}

var trustedClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
}

func elasticRequest(method string, url string, body []byte) (ElasticResponse, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewBuffer(body)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return ElasticResponse{}, err
	}

	req.SetBasicAuth(elasticUsername, elasticPassword)
	req.Header.Set("Content-Type", "application/json")

	resp, err := trustedClient.Do(req)
	if err != nil {
		return ElasticResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return ElasticResponse{}, err
	}

	return ElasticResponse{
		StatusCode: resp.StatusCode,
		Data:       data,
	}, nil
}

func initIndex() {
	mapping := `{
		"mappings": {
			"properties": {
				"title": { "type": "keyword" },
				"developer": { "type": "keyword" },
				"genre": { "type": "keyword" },
				"release_year": { "type": "integer" },
				"rating": { "type": "float" }
			}
		}
	}`
	_, err := elasticRequest("PUT", elasticUrl, []byte(mapping))
	if err != nil {
		log.Print("Error initializing index: ", err)
	}
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	tmpl, _ := template.ParseFiles("index.html")
	tmpl.Execute(w, nil)
}

func addHandler(w http.ResponseWriter, r *http.Request) {
	var g Game
	json.NewDecoder(r.Body).Decode(&g)

	// POST /_doc
	body, _ := json.Marshal(g)
	_, err := elasticRequest("POST", elasticUrl+"/_doc", body)
	if err != nil {
		http.Error(w, "Connection error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func updateHandler(w http.ResponseWriter, r *http.Request) {
	var g Game
	json.NewDecoder(r.Body).Decode(&g)

	// wrap the object in the "doc" field
	updatePayload := map[string]interface{}{
		"doc": g,
	}
	body, _ := json.Marshal(updatePayload)

	// POST /_update/id
	url := fmt.Sprintf("%s/_update/%s", elasticUrl, g.ID)
	resp, _ := elasticRequest("POST", url, body)
	w.WriteHeader(resp.StatusCode)
}

func deleteHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	// DELETE /_doc/id
	resp, _ := elasticRequest("DELETE", elasticUrl+"/_doc/"+id, nil)
	w.WriteHeader(resp.StatusCode)
}

type SearchRequest struct {
	Type  string `json:"type"`
	Field string `json:"field"`
	Value string `json:"value"`
	Min   string `json:"min"`
	Max   string `json:"max"`
}

func searchHandler(w http.ResponseWriter, r *http.Request) {
	var reqBody SearchRequest
	var query string

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if reqBody.Type == "all" || (reqBody.Value == "" && reqBody.Min == "" && reqBody.Max == "") {
		query = `{"query": {"match_all": {}}}`
	} else {
		switch reqBody.Type {
		case "term":
			query = fmt.Sprintf(`{"query": {"term": {"%s": {"value": "%s"}}}}`, reqBody.Field, reqBody.Value)
		case "range":
			min := reqBody.Min
			if min == "" {
				min = "0"
			}
			max := reqBody.Max
			if max == "" {
				max = "99999"
			}
			query = fmt.Sprintf(`{"query": {"range": {"%s": {"gte": %s, "lte": %s}}}}`, reqBody.Field, min, max)
		case "regexp":
			query = fmt.Sprintf(`{"query": {"regexp": {"%s": {"value": "%s"}}}}`, reqBody.Field, reqBody.Value)
		}
	}

	resp, err := elasticRequest("POST", elasticUrl+"/_search", []byte(query))
	if err != nil {
		http.Error(w, "Connection error", http.StatusInternalServerError)
		return
	}

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "Elasticsearch error: "+string(resp.Data), resp.StatusCode)
		return
	}

	var esResult struct {
		Hits struct {
			Hits []struct {
				ID     string `json:"_id"`
				Source Game   `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}

	if err := json.Unmarshal(resp.Data, &esResult); err != nil {
		http.Error(w, "Error parsing Elasticsearch response", http.StatusInternalServerError)
		return
	}

	var results []Game
	for _, hit := range esResult.Hits.Hits {
		hit.Source.ID = hit.ID
		results = append(results, hit.Source)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}
