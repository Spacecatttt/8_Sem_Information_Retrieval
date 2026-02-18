package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type SystemState struct {
	sync.Mutex
	Documents []Document
}

type Document struct {
	Name    string
	Content string
}

type SearchResult struct {
	FileName string  `json:"fileName"`
	Score    float64 `json:"score"`
}

var state = SystemState{
	Documents: []Document{},
}

// Regex to validate document content
var validationRegex = regexp.MustCompile(`^[a-z0-9\s\n\r]+$`)

func main() {
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/api/upload-doc", uploadDocHandler)
	http.HandleFunc("/api/clear-docs", clearDocsHandler)
	http.HandleFunc("/api/search", searchHandler)

	fmt.Println("Server started at http://localhost:8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Println("Error starting server:", err)
	}
}

// the HTML interface
func indexHandler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFiles("index.html")
	if err != nil {
		http.Error(w, "Could not load index.html", http.StatusInternalServerError)
		return
	}
	tmpl.Execute(w, nil)
}

// saves the document content from uploaded files
func uploadDocHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.ParseMultipartForm(10 << 20)
	files := r.MultipartForm.File["documents"]

	state.Lock()
	defer state.Unlock()

	var errorMessages []string

	for _, fileHeader := range files {
		func() {
			file, err := fileHeader.Open()
			if err != nil {
				errorMessages = append(errorMessages, fmt.Sprintf("Error opening %s", fileHeader.Filename))
				return
			}
			defer file.Close()

			contentBytes, err := io.ReadAll(file)
			if err != nil {
				errorMessages = append(errorMessages, fmt.Sprintf("Error reading %s", fileHeader.Filename))
				return
			}

			content := strings.ToLower(string(contentBytes))

			if len(strings.TrimSpace(content)) == 0 {
				errorMessages = append(errorMessages, fmt.Sprintf("File '%s' is empty", fileHeader.Filename))
				return
			}

			// validation characters: a-z, 0-9, whitespace, newlines
			if !validationRegex.MatchString(content) {
				errorMessages = append(errorMessages, fmt.Sprintf("File '%s' ignored: invalid characters.", fileHeader.Filename))
				return // continue
			}

			// check for duplicates by name
			for _, doc := range state.Documents {
				if doc.Name == fileHeader.Filename {
					return
				}
			}
			state.Documents = append(state.Documents, Document{
				Name:    fileHeader.Filename,
				Content: content,
			})
		}()
	}

	docNames := []string{}
	for _, d := range state.Documents {
		docNames = append(docNames, d.Name)
	}

	response := map[string]interface{}{
		"documents": docNames,
		"errors":    errorMessages,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func clearDocsHandler(w http.ResponseWriter, r *http.Request) {
	state.Lock()
	defer state.Unlock()

	state.Documents = []Document{}
	w.WriteHeader(http.StatusOK)
}

// searchHandler processes the search query
func searchHandler(w http.ResponseWriter, r *http.Request) {
	state.Lock()
	defer state.Unlock()

	if len(state.Documents) == 0 {
		http.Error(w, "Error: No documents uploaded. Please add documents first.", http.StatusBadRequest)
		return
	}

	var requestData struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestData); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	response := search(requestData.Query)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func search(query string) []SearchResult {
	fmt.Println("Start searching...")
	results := make([]SearchResult, 0)

	queryTerms := strings.Fields(strings.ToLower(query))
	if len(queryTerms) == 0 {
		return results
	}

	vocabularyMap := make(map[string]bool)

	// add terms from all documents
	for _, doc := range state.Documents {
		for t := range strings.FieldsSeq(doc.Content) {
			vocabularyMap[t] = true
		}
	}

	// add terms from the query
	for _, t := range queryTerms {
		vocabularyMap[t] = true
	}

	// convert map to a slice to have a consistent index for our vectors
	vocabularyList := make([]string, 0, len(vocabularyMap))
	for t := range vocabularyMap {
		vocabularyList = append(vocabularyList, t)
	}

	// create a dummy document for the query to reuse calculateTF
	queryDoc := Document{
		Name:    "query" + fmt.Sprint(time.Now().Unix()),
		Content: strings.Join(queryTerms, " "),
	}

	// calculate query vector
	queryVector := make([]float64, len(vocabularyList))
	for i, term := range vocabularyList {
		tf := calculateTF(term, queryDoc)
		idf := calculateIDF(term, state.Documents) // always 1.0
		queryVector[i] = tf * idf
	}

	fmt.Println("Start calculate document vectors and cosine similarity...")
	// calculate document vectors and cosine similarity
	for _, doc := range state.Documents {
		docVector := make([]float64, len(vocabularyList))
		for i, term := range vocabularyList {
			tf := calculateTF(term, doc)
			idf := calculateIDF(term, state.Documents) // always 1.0
			docVector[i] = tf * idf
		}

		score := calculateCosineSimilarity(queryVector, docVector)

		// filter results by threshold
		if score > 0.0 {
			results = append(results, SearchResult{
				FileName: doc.Name,
				Score:    score,
			})
		}
	}

	// sort results by score in descending order
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	return results
}

func calculateTF(term string, doc Document) float64 {
	terms := strings.Fields(doc.Content)
	totalTerms := len(terms)
	if totalTerms == 0 {
		return 0.0 // prevent division by zero
	}

	termCount := 0
	// count occurrences of the specific term
	for _, t := range terms {
		if t == term {
			termCount++
		}
	}

	//  term occurrences in doc / total terms in doc
	return float64(termCount) / float64(totalTerms)
}

// unary inverse document frequency
func calculateIDF(term string, allDocs []Document) float64 {
	return 1.0
}

// cosine similarity formula
func calculateCosineSimilarity(queryVector []float64, docVector []float64) float64 {
	// vectors must be of the same dimension
	if len(queryVector) != len(docVector) {
		return 0.0
	}

	var dotProduct float64 = 0.0
	var queryMagnitudeSq float64 = 0.0
	var docMagnitudeSq float64 = 0.0

	// calculate dot product and sum of squares for both vectors
	for i := 0; i < len(queryVector); i++ {
		dotProduct += queryVector[i] * docVector[i]
		queryMagnitudeSq += queryVector[i] * queryVector[i]
		docMagnitudeSq += docVector[i] * docVector[i]
	}

	queryMagnitude := math.Sqrt(queryMagnitudeSq)
	docMagnitude := math.Sqrt(docMagnitudeSq)

	// prevent division by zero
	if queryMagnitude == 0.0 || docMagnitude == 0.0 {
		return 0.0
	}

	// formula: dot product / (magnitude of query * magnitude of doc)
	return dotProduct / (queryMagnitude * docMagnitude)
}
