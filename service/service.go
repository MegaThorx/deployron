package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/MegaThorx/deployron/common"
	"github.com/robfig/cron/v3"
)

var config *common.Config

var deployRunner *runner

func main() {
	if err := validateConfigFile("config.yml"); err != nil {
		log.Fatal(err)
	}

	var err error
	config, err = common.MakeConfig("config.yml")
	if err != nil {
		log.Fatalf("Could not load config.yml: %v", err)
	}

	deployRunner = newRunner(executeDeployScript)

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
				runID := deployRunner.trigger(deployment.Name)
				log.Printf("[CRON] Triggered %q (run %d)", deployment.Name, runID)
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

		// One goroutine per connection so a slow or hung client cannot block
		// the accept loop; triggers themselves never block.
		go func() {
			if err := handleConnection(conn); err != nil {
				log.Printf("Could not process service connection: %v", err)
			}
		}()
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

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}

	message, err := common.ReadMessageFrom(conn)
	if err != nil {
		return err
	}
	log.Printf("Received command: %s", message.Identifier)

	return common.WriteMessageTo(conn, processMessage(message))
}

func processMessage(message *common.Message) *common.Message {
	switch message.Identifier {
	case "EXC_DEPLOY":
		if config.FindDeploymentByName(message.Parameter) == nil {
			log.Printf("Invalid deployment name %q passed", message.Parameter)
			return &common.Message{Identifier: "DEPLOY_ERR", Parameter: "unknown deployment"}
		}
		runID := deployRunner.trigger(message.Parameter)
		return &common.Message{Identifier: "DEPLOY_ACK", Parameter: strconv.FormatUint(runID, 10)}

	case "GET_STATUS":
		if config.FindDeploymentByName(message.Parameter) == nil {
			return &common.Message{Identifier: "DEPLOY_ERR", Parameter: "unknown deployment"}
		}
		payload, err := json.Marshal(deployRunner.status(message.Parameter))
		if err != nil {
			log.Printf("Could not encode status for %q: %v", message.Parameter, err)
			return &common.Message{Identifier: "DEPLOY_ERR", Parameter: "status unavailable"}
		}
		return &common.Message{Identifier: "STATUS", Parameter: string(payload)}

	default:
		log.Printf("Unknown command identifier %q", message.Identifier)
		return &common.Message{Identifier: "DEPLOY_ERR", Parameter: "unknown command"}
	}
}

func executeDeployScript(name string) error {
	deployment := config.FindDeploymentByName(name)

	if deployment == nil {
		return fmt.Errorf("invalid deployment name %q passed", name)
	}

	log.Printf("Deployment %q starting", deployment.Name)

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
		return err
	}

	log.Printf("Deployment %q finished", deployment.Name)
	return nil
}
