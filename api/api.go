package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MegaThorx/deployron/common"
	"github.com/gorilla/mux"
)

const (
	maxAuthFailures   = 10
	authFailureWindow = time.Minute
)

// Vars so tests can shrink them.
var (
	waitTimeout      = 15 * time.Minute
	waitPollInterval = 2 * time.Second
)

var config *common.Config

// IPs/CIDRs from api.trusted_proxies, parsed at startup.
var trustedProxies []netip.Prefix

// Replaced in tests so handlers can run without a backend socket.
var callService = callServiceOverSocket

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

	trustedProxies, err = parseTrustedProxies(config.API.TrustedProxies)
	if err != nil {
		log.Fatalf("Invalid api.trusted_proxies entry: %v", err)
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
	router.HandleFunc("/deploy/{name}", deployHandler).Methods(http.MethodPost)
	router.HandleFunc("/deploy/{name}/status", statusHandler).Methods(http.MethodGet)
	return router
}

// authenticate performs the throttle check and secret comparison shared by all
// routes. When it returns ok=false the response has already been written.
func authenticate(res http.ResponseWriter, req *http.Request) (deployName string, ok bool) {
	ip := remoteIP(req)
	if authFailures.blocked(ip) {
		sendJSONError(res, http.StatusTooManyRequests, "Too many failed attempts, try again later")
		return "", false
	}

	deployName = mux.Vars(req)["name"]
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
		return "", false
	}

	return deployName, true
}

func deployHandler(res http.ResponseWriter, req *http.Request) {
	deployName, ok := authenticate(res, req)
	if !ok {
		return
	}

	reply, err := callService("EXC_DEPLOY", deployName)
	if err != nil {
		log.Printf("Could not contact deployment service: %v", err)
		sendJSONError(res, http.StatusBadGateway, "Deployment service unavailable")
		return
	}
	if reply.Identifier != "DEPLOY_ACK" {
		log.Printf("Deployment service rejected %q: %s", deployName, reply.Parameter)
		sendJSONError(res, http.StatusBadGateway, "Deployment service rejected the command")
		return
	}
	runID, err := strconv.ParseUint(reply.Parameter, 10, 64)
	if err != nil {
		log.Printf("Deployment service sent an invalid run ID %q: %v", reply.Parameter, err)
		sendJSONError(res, http.StatusBadGateway, "Deployment service sent an invalid reply")
		return
	}

	if req.URL.Query().Get("wait") == "true" {
		waitForRun(res, req, deployName, runID)
		return
	}

	// The backend executes asynchronously; a 200 only means the command was
	// handed off, not that the deployment succeeded.
	sendJSON(res, http.StatusOK, map[string]any{"status": "queued", "run": runID})
}

// waitForRun blocks until the given run has finished (or waitTimeout passes)
// and reports its outcome, so CI pipelines can rely on the HTTP status code.
func waitForRun(res http.ResponseWriter, req *http.Request, deployName string, runID uint64) {
	// Extend past the server-wide WriteTimeout for this response only.
	// Best-effort: not all ResponseWriters support it (e.g. in tests).
	_ = http.NewResponseController(res).SetWriteDeadline(time.Now().Add(waitTimeout + 30*time.Second))

	deadline := time.Now().Add(waitTimeout)
	for {
		reply, err := callService("GET_STATUS", deployName)
		if err != nil {
			log.Printf("Could not fetch status for %q: %v", deployName, err)
			sendJSONError(res, http.StatusBadGateway, "Deployment service unavailable")
			return
		}
		if reply.Identifier != "STATUS" {
			sendJSONError(res, http.StatusBadGateway, "Deployment service sent an invalid reply")
			return
		}

		var status common.DeployStatus
		if err := json.Unmarshal([]byte(reply.Parameter), &status); err != nil {
			log.Printf("Could not decode status for %q: %v", deployName, err)
			sendJSONError(res, http.StatusBadGateway, "Deployment service sent an invalid reply")
			return
		}

		// Run IDs are per-deployment and monotonic, so once the last finished
		// run is at least ours, our trigger's work is done.
		if status.LastRun >= runID {
			body := map[string]any{"run": runID, "duration_ms": status.DurationMS}
			if status.Last == "success" {
				body["status"] = "success"
				sendJSON(res, http.StatusOK, body)
			} else {
				body["status"] = "failed"
				sendJSON(res, http.StatusBadGateway, body)
			}
			return
		}

		if time.Now().After(deadline) {
			sendJSON(res, http.StatusGatewayTimeout, map[string]any{"status": "timeout", "run": runID})
			return
		}

		select {
		case <-req.Context().Done():
			return
		case <-time.After(waitPollInterval):
		}
	}
}

func statusHandler(res http.ResponseWriter, req *http.Request) {
	deployName, ok := authenticate(res, req)
	if !ok {
		return
	}

	reply, err := callService("GET_STATUS", deployName)
	if err != nil {
		log.Printf("Could not fetch status for %q: %v", deployName, err)
		sendJSONError(res, http.StatusBadGateway, "Deployment service unavailable")
		return
	}
	if reply.Identifier != "STATUS" {
		sendJSONError(res, http.StatusBadGateway, "Deployment service sent an invalid reply")
		return
	}

	res.Header().Set("Content-Type", "application/json")
	res.WriteHeader(http.StatusOK)
	fmt.Fprintln(res, reply.Parameter)
}

func secretsMatch(provided, expected string) bool {
	// Hashing first keeps the comparison length-independent.
	providedSum := sha256.Sum256([]byte(provided))
	expectedSum := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(providedSum[:], expectedSum[:]) == 1
}

func parseTrustedProxies(entries []string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, entry := range entries {
		if prefix, err := netip.ParsePrefix(entry); err == nil {
			prefixes = append(prefixes, prefix)
			continue
		}
		addr, err := netip.ParseAddr(entry)
		if err != nil {
			return nil, fmt.Errorf("%q is neither an IP nor a CIDR", entry)
		}
		prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return prefixes, nil
}

// remoteIP returns the client address used for rate limiting. X-Forwarded-For
// is only honored when the direct peer is a configured trusted proxy; the last
// entry is used because that is the hop the trusted proxy appended.
func remoteIP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}

	forwarded := req.Header.Get("X-Forwarded-For")
	if forwarded == "" || len(trustedProxies) == 0 {
		return host
	}

	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	for _, prefix := range trustedProxies {
		if prefix.Contains(addr) {
			hops := strings.Split(forwarded, ",")
			return strings.TrimSpace(hops[len(hops)-1])
		}
	}
	return host
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

func sendJSON(res http.ResponseWriter, status int, body any) {
	res.Header().Set("Content-Type", "application/json")
	res.WriteHeader(status)
	if err := json.NewEncoder(res).Encode(body); err != nil {
		log.Printf("Could not write response: %v", err)
	}
}

func sendJSONError(res http.ResponseWriter, status int, message string) {
	sendJSON(res, status, map[string]string{"error": message})
}

func callServiceOverSocket(identifier string, parameter string) (*common.Message, error) {
	// A nil local address lets the OS use an unnamed client socket. This avoids
	// stale filesystem sockets and permits concurrent API requests.
	serviceConn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: config.Service.Unixsocket, Net: "unix"})
	if err != nil {
		return nil, err
	}
	defer serviceConn.Close()

	if err := serviceConn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, err
	}

	if err := common.WriteMessageTo(serviceConn, &common.Message{Identifier: identifier, Parameter: parameter}); err != nil {
		return nil, err
	}

	return common.ReadMessageFrom(serviceConn)
}
