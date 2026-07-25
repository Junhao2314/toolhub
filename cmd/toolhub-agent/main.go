package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/toolhub-dev/toolhub/internal/agentclient"
	"github.com/toolhub-dev/toolhub/internal/agentservice"
	"github.com/toolhub-dev/toolhub/internal/domain"
	runtimeadapter "github.com/toolhub-dev/toolhub/internal/runtime"
)

func main() {
	handled, err := agentservice.Run(func(ctx context.Context) error {
		config, err := agentclient.LoadConfig("")
		if err != nil {
			return err
		}
		return agentclient.NewRunner(config).Run(ctx)
	})
	if err != nil {
		log.Fatal(err)
	}
	if handled {
		return
	}
	if len(os.Args) < 2 {
		runAgent("")
		return
	}
	switch os.Args[1] {
	case "enroll":
		enroll(os.Args[2:])
	case "run":
		flags := flag.NewFlagSet("run", flag.ExitOnError)
		configPath := flags.String("config", "", "agent configuration path")
		_ = flags.Parse(os.Args[2:])
		runAgent(*configPath)
	case "scan":
		scan(os.Args[2:])
	case "run-task":
		runTask(os.Args[2:])
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

func enroll(args []string) {
	flags := flag.NewFlagSet("enroll", flag.ExitOnError)
	server := flags.String("server", "", "ToolHub HTTPS URL")
	token := flags.String("token", "", "one-time enrollment token")
	configPath := flags.String("config", "", "agent configuration path")
	home := flags.String("home", "", "runtime home directory")
	dataDir := flags.String("data-dir", "", "ToolHub agent data directory")
	_ = flags.Parse(args)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := agentclient.Enroll(ctx, *server, *token, *configPath, *home, *dataDir)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Enrolled node %s. Agent config: %s\n", config.NodeID, config.ConfigPath)
}

func runAgent(configPath string) {
	config, err := agentclient.LoadConfig(configPath)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := agentclient.NewRunner(config).Run(ctx); err != nil {
		log.Fatal(err)
	}
}

func scan(args []string) {
	flags := flag.NewFlagSet("scan", flag.ExitOnError)
	home := flags.String("home", "", "runtime home directory")
	_ = flags.Bool("json", true, "emit JSON")
	_ = flags.Parse(args)
	if *home == "" {
		var err error
		*home, err = os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
	}
	inventory, err := runtimeadapter.ScanAll(runtimeadapter.DefaultPaths(*home))
	if err != nil {
		log.Fatal(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"runtimes": inventory})
}

func runTask(args []string) {
	flags := flag.NewFlagSet("run-task", flag.ExitOnError)
	configPath := flags.String("config", "", "agent configuration path")
	stdin := flags.Bool("stdin", false, "read signed task from stdin")
	filePath := flags.String("file", "", "read a signed task from a ToolHub SFTP task file")
	_ = flags.Parse(args)
	if !*stdin && *filePath == "" {
		log.Fatal("run-task requires --stdin or --file")
	}
	var reader io.Reader = os.Stdin
	if *filePath != "" {
		if !strings.HasPrefix(filepath.Clean(*filePath), filepath.Clean(os.TempDir())+string(filepath.Separator)+"toolhub-task-") {
			log.Fatal("task file must be a ToolHub task in the system temporary directory")
		}
		file, err := os.Open(*filePath)
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()
		defer os.Remove(*filePath)
		reader = file
	}
	body, err := io.ReadAll(io.LimitReader(reader, 2<<20))
	if err != nil {
		log.Fatal(err)
	}
	var task domain.AgentTask
	if err := json.Unmarshal(body, &task); err != nil {
		log.Fatal(err)
	}
	config, err := agentclient.LoadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	status, result := agentclient.NewExecutor(config).Execute(context.Background(), task)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"status": status, "result": result})
	if status != "succeeded" {
		os.Exit(1)
	}
}
