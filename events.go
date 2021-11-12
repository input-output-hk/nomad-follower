package main

import (
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/hashicorp/nomad/api"
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

type VectorSinkFile struct {
	Type        string                 `toml:"type"`
	Inputs      []string               `toml:"inputs"`
	Path        string                 `toml:"path"`
	Compression string                 `toml:"compression"`
	Encoding    VectorSinkFileEncoding `toml:"encoding"`
}

type VectorSinkFileEncoding struct {
	Codec string `toml:"codec"`
}

type VectorSinkLoki struct {
	Type     string                 `toml:"type"`
	Inputs   []string               `toml:"inputs"`
	Endpoint string                 `toml:"endpoint"`
	Labels   map[string]string      `toml:"labels"`
	Encoding VectorSinkLokiEncoding `toml:"encoding"`
}

type VectorSinkLokiEncoding struct {
	Codec           string   `toml:"codec"`
	TimestampFormat string   `toml:"timestamp_format"`
	OnlyFields      []string `toml:"only_fields"`
}

func (f *nomadFollower) eventListener() error {
	self, err := f.client.Agent().Self()
	die(f.logger, errors.WithMessage(err, "While looking up the local agent"))

	nodeID := self.Stats["client"]["node_id"]
	queryOptions := &api.QueryOptions{Namespace: "default"}
	f.populateAllocs()
	topics := map[api.Topic][]string{api.TopicAllocation: {"*"}}
	index := f.loadIndex()

	eventStream := f.client.EventStream()
	events, err := eventStream.Stream(context.Background(), topics, index, queryOptions)
	die(f.logger, errors.WithMessage(err, "While starting the event stream"))

	f.writeConfig()

	vectorDone := make(chan bool)
	go f.vector(vectorDone)

	for {
		select {
		case <-f.ctx.Done():
			f.logger.Println("Received done, stopping Vector")
			<-vectorDone
			f.logger.Println("Vector finished")
			return nil
		case event := <-events:
			if event.Err != nil {
				return err
			}

			if event.IsHeartbeat() {
				continue
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
	err := os.WriteFile(filepath.Join(f.stateDir, "index"), []byte(sindex), 0644)
	if err != nil {
		f.logger.Printf("Couldn't write index: %s\n", err.Error())
	}
}

func (f *nomadFollower) populateAllocs() {
	f.allocs = &allocations{
		allocs: map[string]*api.Allocation{},
		lock:   &sync.RWMutex{},
	}

	allocs, _, err := f.client.Allocations().List(f.queryOptions)
	die(f.logger, errors.WithMessage(err, "While listing allocations"))

	for _, allocStub := range allocs {
		alloc, _, err := f.client.Allocations().Info(allocStub.ID, f.queryOptions)
		if err != nil {
			log.Fatal(err)
		}
		f.allocs.Add(alloc)
	}
}

func (f *nomadFollower) generateVectorConfig() *VectorConfig {
	sources := map[string]interface{}{}
	transforms := map[string]interface{}{}
	sinks := map[string]interface{}{}
	transformNames := []string{}

	f.allocs.Each(func(id string, alloc *api.Allocation) {
		prefix := fmt.Sprintf(f.allocPrefix, id)

		for _, source := range []string{"stdout", "stderr"} {
			transformNames = append(transformNames, "transform_"+source+"_"+id)

			sources["source_"+source+"_"+id] = VectorSourceFile{
				Type:            "file",
				IgnoreOlderSecs: 300,
				Include:         []string{filepath.Join(prefix, "logs/*."+source+".[0-9]*")},
				LineDelimiter:   "\r\n",
				ReadFrom:        "beginning",
			}

			transforms["transform_"+source+"_"+id] = VectorTransformRemap{
				Inputs: []string{"source_" + source + "_" + id},
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
				`,
			}
		}
	})

	sinks = map[string]interface{}{
		"loki": VectorSinkLoki{
			Type:     "loki",
			Inputs:   transformNames,
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
			},
			Encoding: VectorSinkLokiEncoding{
				Codec:           "text",
				TimestampFormat: "rfc3339",
				OnlyFields:      []string{"message"},
			},
		},
	}

	return &VectorConfig{
		DataDir:    filepath.Join(f.stateDir, "vector"),
		Timezone:   "UTC",
		Sources:    sources,
		Transforms: transforms,
		Sinks:      sinks,
	}
}

func (f *nomadFollower) vector(done chan bool) {
	cmd := exec.Command(
		"vector",
		"--watch-config", f.configFile,
		"--config", f.configFile,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	go func() {
		<-f.ctx.Done()
		f.logger.Println("Killing vector")
		if p := cmd.Process; p != nil {
			p.Signal(syscall.SIGTERM)
			p.Wait()
		}
		done <- true
	}()

	die(f.logger, errors.WithMessage(cmd.Run(), "While running vector"))
}

func (f *nomadFollower) eventHandler(events []api.Event, nodeID string) {
	for _, event := range events {
		switch event.Topic {
		case api.TopicAllocation:
			alloc, err := event.Allocation()
			if err == nil && alloc.NodeID == nodeID {
				f.eventHandleAllocation(alloc)
			}
		}
	}
}

func (f *nomadFollower) eventHandleAllocation(alloc *api.Allocation) {
	switch alloc.ClientStatus {
	case "pending", "running":
		f.allocs.Add(alloc)
		f.writeConfig()
	case "complete":
		go func() {
			// Give Vector time to scoop up all outstanding logs
			time.Sleep(30 * time.Second)
			f.allocs.Del(alloc)
			f.writeConfig()
		}()
	default:
		fmt.Println(alloc.NodeID, alloc.JobID, alloc.ID, alloc.ClientStatus)
	}
}

func (f *nomadFollower) writeConfig() {
	f.configM.Lock()
	defer f.configM.Unlock()

	log.Println("Writing config to", f.configFile)

	buf := bytes.Buffer{}

	if err := toml.NewEncoder(&buf).Encode(f.generateVectorConfig()); err != nil {
		log.Fatal(err)
	}

	if err := ioutil.WriteFile(f.configFile+".new", buf.Bytes(), 0777); err != nil {
		log.Fatal(err)
	}

	if err := os.Rename(f.configFile+".new", f.configFile); err != nil {
		log.Fatal(err)
	}
}
