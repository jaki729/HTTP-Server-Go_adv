package main

import (
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

func main() {
	fmt.Println("Starting server on port 4221...url is http://127.0.0.1:4221")

	// Create a new HTTP server
	http.Handle("/ws", websocket.Handler(handleWebSocket))
	http.HandleFunc("/", handleHTTPRequest)
	http.HandleFunc("/upload", handleFileUpload) // Add file upload endpoint

	// Start the HTTP server
	go func() {
		err := http.ListenAndServe(":4221", nil)
		if err != nil {
			fmt.Println("Failed to start HTTP server:", err)
			os.Exit(1)
		}
	}()

	// Block forever
	select {}
}

// Handles WebSocket connections
func handleWebSocket(ws *websocket.Conn) {
	defer ws.Close()
	fmt.Println("WebSocket connection established")
	// Set CORS headers for WebSocket
	ws.Request().Header.Set("Access-Control-Allow-Origin", "*")

	// Read and write WebSocket messages
	for {
		var message string
		err := websocket.Message.Receive(ws, &message)
		if err != nil {
			fmt.Println("Error receiving WebSocket message:", err)
			break
		}
		fmt.Println("Received WebSocket message:", message)

		// Echo the message back to the client
		err = websocket.Message.Send(ws, "Echo: "+message)
		if err != nil {
			fmt.Println("Error sending WebSocket message:", err)
			break
		}
	}
}

// Handles HTTP requests
func handleHTTPRequest(w http.ResponseWriter, r *http.Request) {
	// Handle compression
	acceptEncoding := r.Header.Get("Accept-Encoding")
	compressResponse := strings.Contains(acceptEncoding, "gzip")

	// Handle Range Requests (partial content)
	rangeHeader := r.Header.Get("Range")
	var rangeStart, rangeEnd int
	if rangeHeader != "" {
		// Example: "bytes=0-499"
		fmt.Sscanf(rangeHeader, "bytes=%d-%d", &rangeStart, &rangeEnd)
	}

	// Handle different HTTP methods
	switch r.Method {
	case "GET":
		handleGetRequest(w, r.URL.Path, compressResponse, rangeStart, rangeEnd, r.Header)
	case "POST":
		handlePostRequest(w)
	case "PUT":
		handlePutRequest(w)
	case "DELETE":
		handleDeleteRequest(w)
	case "HEAD":
		handleHeadRequest(w)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("405 Method Not Allowed"))
	}
}

func enableCORS(w http.ResponseWriter) {
    w.Header().Set("Access-Control-Allow-Origin", "http://127.0.0.1:5500")
    w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
    w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

// Handle file upload
func handleFileUpload(w http.ResponseWriter, r *http.Request) {
	enableCORS(w) // Enable CORS for this endpoint
    if r.Method == "OPTIONS" {
        return
    }
	// Parse the multipart form
	err := r.ParseMultipartForm(10 << 20) // 10 MB limit
	if err != nil {
		fmt.Println("Error parsing form:", err)
		http.Error(w, `{"error": "Unable to parse form"}`, http.StatusBadRequest)
		return
	}

	// Retrieve the file from the form
	file, handler, err := r.FormFile("file")
	if err != nil {
		fmt.Println("Error retrieving file:", err)
		http.Error(w, `{"error": "Unable to retrieve file"}`, http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Create a new file in the server's uploads directory
	uploadsDir := filepath.Join(".", "uploads") // Relative path to the uploads directory
	if err := os.MkdirAll(uploadsDir, os.ModePerm); err != nil {
		fmt.Println("Error creating uploads directory:", err)
		http.Error(w, `{"error": "Unable to create uploads directory"}`, http.StatusInternalServerError)
		return
	}

	filePath := filepath.Join(uploadsDir, handler.Filename)
	dst, err := os.Create(filePath)
	if err != nil {
		fmt.Println("Error creating file:", err)
		http.Error(w, `{"error": "Unable to create file"}`, http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	// Copy the uploaded file to the server
	_, err = io.Copy(dst, file)
	if err != nil {
		fmt.Println("Error saving file:", err)
		http.Error(w, `{"error": "Unable to save file"}`, http.StatusInternalServerError)
		return
	}

	// Respond with success message in JSON format
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf(`{"message": "File uploaded successfully: %s"}`, handler.Filename)))
}

// Handle GET request with Range, Compression, and HTTP Caching
func handleGetRequest(w http.ResponseWriter, path string, compressResponse bool, rangeStart, rangeEnd int, headers http.Header) {
	enableCORS(w)
	var filePath string

	if path == "/" {
		filePath = filepath.Join(".", "index.html") // Relative path to index.html
	} else {
		filePath = filepath.Join(".", path[1:]) // Relative path to other files
		if _, err := os.Stat(filePath); os.IsNotExist(err) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("404 Not Found"))
			return
		}
	}

	// Add Caching Headers (Cache-Control, ETag)
	if modTime, err := getFileModTime(filePath); err == nil {
		w.Header().Set("Last-Modified", modTime.Format(time.RFC1123))
	}

	etag := getFileETag(filePath)
	w.Header().Set("ETag", etag)
	if ifNoneMatch := headers.Get("If-None-Match"); ifNoneMatch == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	serveFile(w, filePath, compressResponse, rangeStart, rangeEnd)
}
// Get file modification time
func getFileModTime(filePath string) (time.Time, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return time.Time{}, err
	}
	return info.ModTime(), nil
}

// Get the ETag of a file
func getFileETag(filePath string) string {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return ""
	}
	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:])
}

// Serve file with optional Gzip compression and range requests
func serveFile(w http.ResponseWriter, filePath string, compressResponse bool, rangeStart, rangeEnd int) {
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Println("Error opening file:", err)
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("404 Not Found"))
		return
	}
	defer file.Close()

	if rangeStart > 0 || rangeEnd > 0 {
		// Serve partial content based on range
		file.Seek(int64(rangeStart), io.SeekStart)
	}

	// Optionally compress file using gzip
	if compressResponse {
		w.Header().Set("Content-Encoding", "gzip")
		gzipWriter := gzip.NewWriter(w)
		defer gzipWriter.Close()

		if rangeEnd > 0 {
			io.CopyN(gzipWriter, file, int64(rangeEnd-rangeStart))
		} else {
			io.Copy(gzipWriter, file)
		}
	} else {
		if rangeEnd > 0 {
			io.CopyN(w, file, int64(rangeEnd-rangeStart))
		} else {
			io.Copy(w, file)
		}
	}
}

// Handle POST request (receive data and return acknowledgment)
func handlePostRequest(w http.ResponseWriter) {
	enableCORS(w)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("POST method received"))
}

// Handle PUT request (mock resource replacement)
func handlePutRequest(w http.ResponseWriter) {
	enableCORS(w)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("PUT method received, resource replaced"))
}

// Handle DELETE request (mock resource deletion)
func handleDeleteRequest(w http.ResponseWriter) {
	enableCORS(w)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("DELETE method received, resource deleted"))
}

// Handle HEAD request (return only headers)
func handleHeadRequest(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	w.Header().Set("Content-Type", "text/html")
}