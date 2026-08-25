package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/MegaThorx/deployron/common"
	"github.com/gorilla/mux"
)

const (
	maxAuthFailures   = 10
	authFailureWindow = time.Minute
)

var config *common.Config

// Replaced in tests so the handler can run without a backend socket.
var sendMessageToService = sendMessageToServiceOverSocket

// Compared against when the deployment is unknown, so both failure paths
// perform the same work and stay indistinguishable from the outside.
var dummySecret = func() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		log.Fatalf("Could not generate dummy secret: %v", err)
	}
	return hex.EncodeToString(buf)
}()

var authFailures = newAuthThrottle()

func main() {
	var err error
	config, err = common.MakeConfig("config.yml")
	if err != nil {
		log.Fatalf("Could not load config.yml: %v", err)
	}

	addr := fmt.Sprintf("%s:%d", config.API.IP, config.API.Port)
	server := &http.Server{
		Addr:              addr,
		Handler:           newRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	fmt.Printf("Listening on %s\n", addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func newRouter() *mux.Router {
	router := mux.NewRouter()
	router.HandleFunc("/deploy/{name}", apiHandler).Methods(http.MethodPost)
	return router
}

func apiHandler(res http.ResponseWriter, req *http.Request) {
	ip := remoteIP(req)
	if authFailures.blocked(ip) {
		sendJSONError(res, http.StatusTooManyRequests, "Too many failed attempts, try again later")
		return
	}

	deployName := mux.Vars(req)["name"]
	var deployment *common.Deployment
	if len(deployName) <= common.MaxParameterLength {
		deployment = config.FindDeploymentByName(deployName)
	}

	secret := req.Header.Get("X-API-Secret")

	expected := dummySecret
	if deployment != nil {
		expected = deployment.Secret
	}

	// Unknown name and wrong secret must be indistinguishable, otherwise
	// deployment names can be enumerated without authentication.
	if !secretsMatch(secret, expected) || deployment == nil {
		authFailures.recordFailure(ip)
		sendJSONError(res, http.StatusNotFound, "Unknown deployment or wrong secret")
		return
	}

	if err := sendMessageToService("EXC_DEPLOY", deployName); err != nil {
		log.Printf("Could not contact deployment service: %v", err)
		sendJSONError(res, http.StatusBadGateway, "Deployment service unavailable")
		return
	}

	// The backend executes asynchronously; a 200 only means the command was
	// handed off, not that the deployment succeeded.
	res.Header().Set("Content-Type", "application/json")
	res.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(res).Encode(struct {
		Status string `json:"status"`
	}{Status: "deployment queued"}); err != nil {
		log.Printf("Could not write response: %v", err)
	}
}

func secretsMatch(provided, expected string) bool {
	// Hashing first keeps the comparison length-independent.
	providedSum := sha256.Sum256([]byte(provided))
	expectedSum := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(providedSum[:], expectedSum[:]) == 1
}

func remoteIP(req *http.Request) string {
	if host, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		return host
	}
	return req.RemoteAddr
}

type authThrottle struct {
	mu      sync.Mutex
	entries map[string]*throttleEntry
}

type throttleEntry struct {
	windowStart time.Time
	failures    int
}

func newAuthThrottle() *authThrottle {
	return &authThrottle{entries: make(map[string]*throttleEntry)}
}

func (t *authThrottle) blocked(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.entries[ip]
	if !ok {
		return false
	}
	if time.Since(entry.windowStart) > authFailureWindow {
		delete(t.entries, ip)
		return false
	}
	return entry.failures >= maxAuthFailures
}

func (t *authThrottle) recordFailure(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Bound memory when failures come from many different addresses.
	if len(t.entries) > 4096 {
		for key, entry := range t.entries {
			if time.Since(entry.windowStart) > authFailureWindow {
				delete(t.entries, key)
			}
		}
	}

	entry, ok := t.entries[ip]
	if !ok || time.Since(entry.windowStart) > authFailureWindow {
		t.entries[ip] = &throttleEntry{windowStart: time.Now(), failures: 1}
		return
	}
	entry.failures++
}

func sendJSONError(res http.ResponseWriter, status int, message string) {
	type Error struct {
		Error string `json:"error"`
	}

	res.Header().Set("Content-Type", "application/json")
	res.WriteHeader(status)
	if err := json.NewEncoder(res).Encode(Error{Error: message}); err != nil {
		log.Printf("Could not write error response: %v", err)
	}
}

func sendMessageToServiceOverSocket(identifier string, parameter string) error {
	payload, err := common.WriteMessage(&common.Message{Identifier: identifier, Parameter: parameter})
	if err != nil {
		return err
	}

	// A nil local address lets the OS use an unnamed client socket. This avoids
	// stale filesystem sockets and permits concurrent API requests.
	serviceConn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: config.Service.Unixsocket, Net: "unix"})
	if err != nil {
		return err
	}
	defer serviceConn.Close()

	written, err := serviceConn.Write(payload)
	if err != nil {
		return err
	}
	if written != len(payload) {
		return io.ErrShortWrite
	}

	return nil
}
