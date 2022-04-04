package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	nomad "github.com/hashicorp/nomad/api"
	"github.com/pkg/errors"
)

type VectorConfig struct {
	DataDir    string                 `toml:"data_dir"`
	Timezone   string                 `toml:"timezone"`
	Sources    map[string]interface{} `toml:"sources"`
	Transforms map[string]interface{} `toml:"transforms"`
	Sinks      map[string]interface{} `toml:"sinks"`
}

type VectorSourceJournald struct {
	Type          string `toml:"type"`
	RemapPriority bool   `toml:"remap_priority"`
}

type VectorSourceFile struct {
	Type            string   `toml:"type"`
	IgnoreOlderSecs uint64   `toml:"ignore_older_secs"`
	Include         []string `toml:"include"`
	LineDelimiter   string   `toml:"line_delimiter"`
	ReadFrom        string   `toml:"read_from"`
}

type VectorSourceHostMetrics struct {
	Type       string   `toml:"type"`
	Collectors []string `toml:"collectors"`
}

type VectorTransformRemap struct {
	Type   string   `toml:"type"`
	Inputs []string `toml:"inputs"`
	Source string   `toml:"source"`
}

type VectorSinkConsole struct {
	Type     string                    `toml:"type"`
	Inputs   []string                  `toml:"inputs"`
	Target   string                    `toml:"target"`
	Encoding VectorSinkConsoleEncoding `toml:"encoding"`
}

type VectorSinkConsoleEncoding struct {
	Codec string `toml:"codec"`
}

type VectorSink struct {
	Type   string   `toml:"type"`
	Inputs []string `toml:"inputs"`
}

type VectorSinkFile struct {
	VectorSink
	Path        string                 `toml:"path"`
	Compression string                 `toml:"compression"`
	Encoding    VectorSinkFileEncoding `toml:"encoding"`
}

type VectorSinkFileEncoding struct {
	Codec string `toml:"codec"`
}

type VectorSinkLoki struct {
	VectorSink
	Endpoint string                 `toml:"endpoint"`
	Labels   map[string]string      `toml:"labels"`
	Encoding VectorSinkLokiEncoding `toml:"encoding"`
}

type VectorSinkPrometheus struct {
	VectorSink
	Endpoint    string      `toml:"endpoint"`
	Healthcheck Healthcheck `toml:"healthcheck"`
}

type Healthcheck struct {
	Enabled bool `toml:"enabled"`
}

type VectorSinkLokiEncoding struct {
	Codec           string   `toml:"codec"`
	TimestampFormat string   `toml:"timestamp_format"`
	OnlyFields      []string `toml:"only_fields"`
}

func (f *nomadFollower) start() error {
	if f.nomadTokenFile != "" {
		if token, err := f.tokenFromEnvironment(); err != nil {
			return errors.WithMessage(err, "while getting token from environment")
		} else {
			f.nomadClient.SetSecretID(token)
		}
		go f.reloadToken()
		go f.checkToken()
	}
	go f.configWriter()
	go f.listenLoop()

	f.logger.Printf("Sending to Loki: %s and Prometheus: %s", f.lokiUrl, f.prometheusUrl)
	f.logger.Println("Waiting for valid Vector configuration")
	<-f.vectorStart
	f.logger.Println("Starting Vector")
	return f.vector()
}

func (f *nomadFollower) reloadToken() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)

	for range c {
		f.logger.Println("Reloading Token because of HUP Signal")
		if token, err := f.tokenFromEnvironment(); err != nil {
			f.logger.Fatal(errors.WithMessage(err, "while getting token from environment"))
		} else {
			f.tokenUpdated <- token
		}
	}
}

func (f *nomadFollower) tokenFromEnvironment() (string, error) {
	if b, err := os.ReadFile(f.nomadTokenFile); err != nil {
		return "", errors.WithMessagef(err, "Failed to read the nomad token file at %s", f.nomadTokenFile)
	} else {
		f.logger.Println("Nomad Token found")
		return strings.TrimSpace(string(b)), nil
	}
}

// Just checks that the current token is still fine regularly
func (f *nomadFollower) checkToken() {
	for range time.NewTimer(30 * time.Minute).C {
		if currentToken, _, err := f.nomadClient.ACLTokens().Self(nil); err != nil {
			f.logger.Fatal(err)
		} else {
			f.logger.Printf("Current Token: %s %v\n", currentToken.Name, currentToken.Policies)
		}
	}
}

// Listen to Nomad events and update Vector config accordingly
func (f *nomadFollower) listenLoop() error {
	for {
		if err := f.listen(); err != nil {
			f.logger.Println(errors.WithMessage(err, "while listening"))
			time.Sleep(10 * time.Second)
		}
	}
}

func (f *nomadFollower) NodeID() (string, error) {
	self, err := f.nomadClient.Agent().Self()
	if err != nil {
		return "", errors.WithMessage(err, "While looking up the local agent")
	}

	return self.Stats["client"]["node_id"], nil
}

// Listen to Nomad events and update Vector config accordingly
func (f *nomadFollower) listen() error {
	nodeID, err := f.NodeID()
	if err != nil {
		return err
	}
	f.populateAllocs(nodeID)

	topics := map[nomad.Topic][]string{nomad.TopicAllocation: {"*"}}
	index := f.loadIndex()
	eventStream := f.nomadClient.EventStream()
	events, err := eventStream.Stream(context.Background(), topics, index, f.queryOptions)
	if err != nil {
		return errors.WithMessage(err, "While starting the event stream")
	}

	f.configUpdated <- true

	f.logger.Println("start listening to nomad events")

	for {
		select {
		case token := <-f.tokenUpdated:
			f.nomadClient.SetSecretID(token)
			return nil
		case event := <-events:
			if event.Err != nil {
				return err
			}

			f.saveIndex(event.Index)
			f.eventHandler(event.Events, nodeID)
		}
	}
}

func (f *nomadFollower) loadIndex() uint64 {
	bindex, err := os.ReadFile(filepath.Join(f.stateDir, "index"))
	if err != nil {
		return 0
	}
	index, err := strconv.ParseUint(string(bindex), 10, 64)
	if err != nil {
		return 0
	}
	return index
}

func (f *nomadFollower) saveIndex(index uint64) {
	sindex := strconv.FormatUint(index, 10)
	err := os.WriteFile(filepath.Join(f.stateDir, "index"), []byte(sindex), 0o644)
	if err != nil {
		f.logger.Printf("Couldn't write index: %s\n", err.Error())
	}
}

func (f *nomadFollower) populateAllocs(nodeID string) {
	f.allocs = &allocations{m: &sync.Map{}}

	allocs, _, err := f.nomadClient.Allocations().List(f.queryOptions)
	die(f.logger, errors.WithMessage(err, "While listing allocations"))

	for _, allocStub := range allocs {
		if allocStub.NodeID != nodeID {
			continue
		}

		prefix := fmt.Sprintf(f.allocPrefix, allocStub.ID)
		_, err := os.Stat(prefix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			f.logger.Println(errors.WithMessage(err, "checking alloc prefix"))
			continue
		}

		options := &nomad.QueryOptions{Namespace: allocStub.Namespace}
		if alloc, _, err := f.nomadClient.Allocations().Info(allocStub.ID, options); err != nil {
			f.logger.Fatal(err)
		} else {
			f.allocs.Add(alloc)
			switch allocStub.ClientStatus {
			case "pending", "running":
			case "complete", "failed":
				// So this might not be recorded yet, we give it 10 minutes to do that.
				go func(*nomad.Allocation) {
					time.Sleep(10 * time.Minute)
					f.allocs.Del(alloc)
				}(alloc)
			default:
				f.logger.Printf("Ignore alloc %s : status = %s\n", allocStub.ID, allocStub.ClientStatus)
			}
		}

		f.configUpdated <- true
	}
}

func (f *nomadFollower) generateVectorConfig() *VectorConfig {
	sources := map[string]interface{}{
		"host": VectorSourceHostMetrics{
			Type:       "host_metrics",
			Collectors: []string{"cgroups", "cpu", "disk", "filesystem", "load", "host", "memory", "network"},
		},
	}
	transforms := map[string]interface{}{
		"transform_host": VectorTransformRemap{
			Inputs: []string{"host"},
			Type:   "remap",
			Source: `
			if is_string(.tags.cgroup) {
				.tags.cgroup, err = replace(.tags.cgroup, "\\x2d", "-")
				if err != null {
					log(., level: "error")
					log(err, level: "error")
				}
			}
			`,
		},
	}

	f.allocs.Each(func(id string, alloc *nomad.Allocation) {
		prefix := fmt.Sprintf(f.allocPrefix, id)

		taskNames := []string{}
		for name := range alloc.TaskResources {
			taskNames = append(taskNames, name)
		}

		for _, taskName := range taskNames {
			for _, source := range []string{"stdout", "stderr"} {
				sourceName := "source_" + source + "_" + taskName + "_" + id
				sources[sourceName] = VectorSourceFile{
					Type:            "file",
					IgnoreOlderSecs: 300,
					Include:         []string{filepath.Join(prefix, "logs/"+taskName+"."+source+".[0-9]*")},
					LineDelimiter:   "\n",
					ReadFrom:        "beginning",
				}

				transforms["transform_"+source+"_"+taskName+"_"+id] = VectorTransformRemap{
					Inputs: []string{sourceName},
					Type:   "remap",
					Source: `
					.source = "` + source + `"
					.nomad_alloc_id = "` + alloc.ID + `"
					.nomad_job_id = "` + alloc.JobID + `"
					.nomad_alloc_name = "` + alloc.Name + `"
					.nomad_namespace = "` + alloc.Namespace + `"
					.nomad_node_id = "` + alloc.NodeID + `"
					.nomad_node_name = "` + alloc.NodeName + `"
					.nomad_task_group = "` + alloc.TaskGroup + `"
					.nomad_task_name = "` + taskName + `"
				`,
				}
			}
		}
	})

	sinks := map[string]interface{}{
		"prometheus": VectorSinkPrometheus{
			VectorSink: VectorSink{
				Type:   "prometheus_remote_write",
				Inputs: []string{"transform_host"},
			},
			Endpoint: f.prometheusUrl,
			// VictoriaMetrics doesn't respond with 200 on GET to this endpoint
			Healthcheck: Healthcheck{Enabled: false},
		},
	}

	if len(transforms) > 1 {
		sinks["loki"] = VectorSinkLoki{
			VectorSink: VectorSink{
				Type:   "loki",
				Inputs: []string{"transform_stdout_*", "transform_stderr_*"},
			},
			Endpoint: f.lokiUrl,
			Labels: map[string]string{
				"source":           "{{ source }}",
				"nomad_alloc_id":   "{{ nomad_alloc_id }}",
				"nomad_job_id":     "{{ nomad_job_id }}",
				"nomad_alloc_name": "{{ nomad_alloc_name }}",
				"nomad_namespace":  "{{ nomad_namespace }}",
				"nomad_node_id":    "{{ nomad_node_id }}",
				"nomad_node_name":  "{{ nomad_node_name }}",
				"nomad_task_group": "{{ nomad_task_group }}",
				"nomad_task_name":  "{{ nomad_task_name }}",
			},
			Encoding: VectorSinkLokiEncoding{
				Codec:           "text",
				TimestampFormat: "rfc3339",
				OnlyFields:      []string{"message"},
			},
		}
	}

	return &VectorConfig{
		DataDir:    filepath.Join(f.stateDir, "vector"),
		Timezone:   "UTC",
		Sources:    sources,
		Transforms: transforms,
		Sinks:      sinks,
	}
}

func (f *nomadFollower) vector() error {
	cmd := exec.Command(
		"vector",
		"--watch-config", f.configFile,
		"--config-toml", f.configFile,
		"--quiet",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (f *nomadFollower) eventHandler(events []nomad.Event, nodeID string) {
	for _, event := range events {
		if event.Topic == nomad.TopicAllocation {
			alloc, err := event.Allocation()
			if err == nil && alloc.NodeID == nodeID {
				f.eventHandleAllocation(alloc)
			}
		}
	}
}

func (f *nomadFollower) eventHandleAllocation(alloc *nomad.Allocation) {
	switch alloc.ClientStatus {
	case "pending", "running":
		f.allocs.Add(alloc)
		f.configUpdated <- true
	case "complete", "failed":
		go func() {
			// Give Vector time to scoop up all outstanding logs
			time.Sleep(30 * time.Second)
			f.allocs.Del(alloc)
			f.configUpdated <- true
		}()
	default:
		fmt.Println(alloc.NodeID, alloc.JobID, alloc.ID, alloc.ClientStatus)
	}
}

func (f *nomadFollower) configWriter() {
	var config *VectorConfig
	for {
		select {
		case <-f.configUpdated:
			generatedConfig := f.generateVectorConfig()
			if len(generatedConfig.Sources) == 0 {
				config = nil
				continue
			}
			config = generatedConfig
		case <-time.After(1 * time.Second):
			if config == nil {
				continue
			}
			f.logger.Printf("Update config with %d sources", len(config.Sources))
			if err := f.doWriteConfig(config); err != nil {
				f.logger.Printf("Failed writing config: %s", err.Error())
			}
			config = nil
		}
	}
}

func (f *nomadFollower) doWriteConfig(config *VectorConfig) error {
	f.logger.Println("Writing config to", f.configFile)

	buf := &bytes.Buffer{}

	if err := toml.NewEncoder(buf).Encode(config); err != nil {
		return errors.WithMessage(err, "While encoding the vector config")
	} else if err := os.WriteFile(f.configFile+".new", buf.Bytes(), 0o777); err != nil {
		return errors.WithMessage(err, "while writing the new config file")
	} else if err := f.validateNewConfig(); err != nil {
		return errors.WithMessage(err, "while validating vector config")
	} else if err := os.Rename(f.configFile+".new", f.configFile); err != nil {
		return errors.WithMessage(err, "while replacing the old config")
	} else if !f.vectorStarted {
		f.vectorStarted = true
		f.vectorStart <- true
	}

	f.logger.Println("Config written to", f.configFile)
	return nil
}

func (f *nomadFollower) validateNewConfig() error {
	cmd := exec.Command("vector", "validate", f.configFile+".new")
	output := &bytes.Buffer{}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		f.logger.Println(string(output.String()))
		return err
	}
	return nil
}
