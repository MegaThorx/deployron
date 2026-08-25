package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"

	"github.com/MegaThorx/deployron/common"
	"github.com/gorilla/mux"
)

var config *common.Config

func main() {
	var err error
	config, err = common.MakeConfig("config.yml")
	if err != nil {
		log.Fatalf("Could not load config.yml: %v", err)
	}

	router := mux.NewRouter()
	router.HandleFunc("/deploy/{name}", apiHandler)

	fmt.Printf("Listening on %s:%d\n", config.API.IP, config.API.Port)
	if err := http.ListenAndServe(fmt.Sprintf("%s:%d", config.API.IP, config.API.Port), router); err != nil {
		log.Fatal(err)
	}
}

func apiHandler(res http.ResponseWriter, req *http.Request) {
	params := mux.Vars(req)
	deployName := params["name"]
	deployment := config.FindDeploymentByName(deployName)

	// Check if there is a deployment with this name
	if deployment == nil {
		sendJSONError(res, http.StatusNotFound, "Unknown deployment")
		return
	}

	// Check API secrets
	if req.URL.Query().Get("APISecret") != deployment.Secret {
		sendJSONError(res, http.StatusForbidden, "Wrong deploy secret")
		return
	}

	if err := sendMessageToService("EXC_DEPLOY", deployName); err != nil {
		log.Printf("Could not contact deployment service: %v", err)
		sendJSONError(res, http.StatusBadGateway, "Deployment service unavailable")
		return
	}

	res.WriteHeader(http.StatusOK)
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

func sendMessageToService(identifier string, parameter string) error {
	payload := common.WriteMessage(&common.Message{Identifier: identifier, Parameter: parameter})

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
