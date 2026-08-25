package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/MegaThorx/deployron/common"
	"github.com/robfig/cron/v3"
)

var config *common.Config

// Tracks deployments that are currently executing so overlapping runs of the
// same deployment (e.g. a slow script plus a fast cron schedule) are skipped.
var runningDeployments sync.Map

func main() {
	if err := validateConfigFile("config.yml"); err != nil {
		log.Fatal(err)
	}

	var err error
	config, err = common.MakeConfig("config.yml")
	if err != nil {
		log.Fatalf("Could not load config.yml: %v", err)
	}

	// Remove old sockets (in case the service crashed)
	if err := os.Remove(config.Service.Unixsocket); err != nil && !os.IsNotExist(err) {
		log.Fatalf("Could not remove stale service socket: %v", err)
	}

	// Start unix socket
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: config.Service.Unixsocket, Net: "unix"})
	if err != nil {
		log.Fatal(err)
	}
	defer os.Remove(config.Service.Unixsocket)
	defer l.Close()

	// The backend runs with group deployron under systemd, so only root and the
	// API service account can access the command socket.
	if err := os.Chmod(config.Service.Unixsocket, 0o660); err != nil {
		log.Fatalf("Could not set service socket permissions: %v", err)
	}

	// Register cron jobs
	scheduler := cron.New()
	for _, deployment := range config.Deployments {
		if deployment.CronDeploy != "" {
			if _, err := scheduler.AddFunc(deployment.CronDeploy, func() {
				log.Printf("[CRON] Launching %q", deployment.Name)
				executeDeployScript(deployment.Name)
			}); err != nil {
				log.Fatalf("Invalid cron schedule for %q: %v", deployment.Name, err)
			}
		}
	}
	scheduler.Start()
	defer scheduler.Stop()

	log.Println("Waiting for commands")

	// Wait for commands
	for {
		// Accept incoming connection
		conn, err := l.AcceptUnix()
		if err != nil {
			log.Printf("Could not accept service connection: %v", err)
			continue
		}

		if err := handleConnection(conn); err != nil {
			log.Printf("Could not process service connection: %v", err)
		}
	}
}

func validateConfigFile(path string) error {
	statInfo, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("could not stat %s: %w", path, err)
	}

	stat, ok := statInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("could not inspect ownership of %s", path)
	}

	// Group read is allowed for deployron_api.service. Group write/execute and
	// all access by others are rejected because the backend can run commands as root.
	if stat.Uid != 0 || statInfo.Mode().Perm()&0o037 != 0 {
		return fmt.Errorf("%s must be owned by root and have permissions 0640 or stricter", path)
	}

	return nil
}

func handleConnection(conn *net.UnixConn) error {
	defer conn.Close()

	var buf [common.MessageSize]byte
	if _, err := io.ReadFull(conn, buf[:]); err != nil {
		return err
	}

	message := common.ReadMessage(buf)
	log.Printf("Received command: %s", message.Identifier)

	// Process asynchronously so a long-running deployment does not block the
	// accept loop.
	go processMessage(message)

	return nil
}

func processMessage(message *common.Message) {
	switch message.Identifier {
	case "EXC_DEPLOY":
		executeDeployScript(message.Parameter)
	default:
		log.Printf("Unknown command identifier %q", message.Identifier)
	}
}

func executeDeployScript(name string) {
	deployment := config.FindDeploymentByName(name)

	if deployment == nil {
		log.Printf("Invalid deployment name %q passed", name)
		return
	}

	if _, alreadyRunning := runningDeployments.LoadOrStore(deployment.Name, struct{}{}); alreadyRunning {
		log.Printf("Deployment %q is already running, skipping this run", deployment.Name)
		return
	}
	defer runningDeployments.Delete(deployment.Name)

	// Fail fast: abort the deployment as soon as any step fails.
	var commandBuffer bytes.Buffer
	commandBuffer.WriteString("set -euo pipefail; ")
	for _, line := range deployment.Script {
		commandBuffer.WriteString(line)
		commandBuffer.WriteString("; ")
	}

	// Prepare deploy script for execution
	cmd := exec.Command("su", "-s", "/bin/bash", "-c", commandBuffer.String(), deployment.User)

	// Redirect stdout, stderr
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Run deploy script
	if err := cmd.Run(); err != nil {
		log.Printf("Deployment %q failed: %v", deployment.Name, err)
	}
}
