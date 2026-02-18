package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

type SystemState struct {
	sync.Mutex
	Terms     []string
	Documents []Document
}

type Document struct {
	Name    string
	Content string
}

var state = SystemState{
	Terms:     []string{},
	Documents: []Document{},
}

// Regex to validate document content
var validationRegex = regexp.MustCompile(`^[a-z0-9\s\n\r]+$`)

func main() {
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/api/update-terms", updateTermsHandler)
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

// saves the terms from the text area
func updateTermsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var requestData struct {
		RawTerms string `json:"raw_terms"`
	}

	if err := json.NewDecoder(r.Body).Decode(&requestData); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	state.Lock()
	defer state.Unlock()

	// Normalize: lowercase and split by whitespace
	normalized := strings.ToLower(requestData.RawTerms)
	state.Terms = strings.Fields(normalized)

	fmt.Printf("[Log] Terms updated. Count: %d\n", len(state.Terms))
	w.WriteHeader(http.StatusOK)
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

	// Формуємо відповідь
	docNames := []string{}
	for _, d := range state.Documents {
		docNames = append(docNames, d.Name)
	}

	// Створюємо структуру відповіді
	response := map[string]interface{}{
		"documents": docNames,
		"errors":    errorMessages,
	}

	w.Header().Set("Content-Type", "application/json")
	// Повертаємо 200 OK, навіть якщо були помилки, бо сервер обробив запит коректно
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

	if len(state.Terms) == 0 {
		http.Error(w, "Error: No terms defined. Please enter terms first.", http.StatusBadRequest)
		return
	}
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

	response := booleanSearch(requestData.Query)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// boolean search logic (DNF)
func booleanSearch(query string) []string {
	query = strings.ToLower(query)

	// Split OR
	conjuncts := strings.Split(query, " or ")
	finalResultMap := make(map[string]bool)

	for _, conjunct := range conjuncts {
		// AND-group
		conjunctDocs := evaluateConjunct(conjunct)

		// merge conjunct results
		for docName := range conjunctDocs {
			finalResultMap[docName] = true
		}
	}

	var response []string
	for docName := range finalResultMap {
		response = append(response, docName)
	}

	return response
}

// AND-group and returns the intersection of document sets
func evaluateConjunct(conjunct string) map[string]bool {
	// Split AND operation
	parts := strings.Split(conjunct, "and")

	var conjunctResult map[string]bool
	firstTerm := true

	for _, term := range parts {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}

		isNot := false
		// Check for NOT(...) syntax
		if strings.HasPrefix(term, "not(") && strings.HasSuffix(term, ")") {
			isNot = true
			term = strings.TrimPrefix(term, "not(")
			term = strings.TrimSuffix(term, ")")
			term = strings.TrimSpace(term)
		}

		// Find documents for this specific term
		termDocs := getDocsForTerm(term, isNot)

		// Intersection Logic (AND)
		if firstTerm {
			conjunctResult = termDocs
			firstTerm = false
		} else {
			intersected := make(map[string]bool)
			for docName := range conjunctResult {
				if termDocs[docName] {
					intersected[docName] = true // append
				}
			}
			conjunctResult = intersected
		}
	}

	if conjunctResult == nil {
		return make(map[string]bool)
	}
	return conjunctResult
}

// returns document names that match the term criteria
func getDocsForTerm(term string, isNot bool) map[string]bool {
	resultSet := make(map[string]bool)

	for _, doc := range state.Documents {
		words := strings.Fields(doc.Content)
		contains := false

		for _, w := range words {
			if w == term {
				contains = true
				break
			}
		}

		// If isNot is true => document with: contains == false
		if contains != isNot {
			resultSet[doc.Name] = true
		}
	}
	return resultSet
}
