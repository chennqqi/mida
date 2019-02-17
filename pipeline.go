package main

import (
	log "github.com/sirupsen/logrus"
	"os"
	"sync"
)

// A configuration for running MIDA
type MIDAConfig struct {
	// Number of simultaneous browser instances
	NumCrawlers int

	// Number of goroutines storing results data
	NumStorers int

	// If true, TaskLocation is an address for an AMPQ server, and credentials
	// must also be provided (as part of the URI). If false, TaskLocation
	// will be the path to the JSON file we will use to crawl (autogenerated: "MIDA_task.json")
	UseAMPQForTasks bool
	TaskLocation    string

	// Monitoring parameters
	EnableMonitoring bool
	PrometheusPort   int
}

func InitPipeline() {

	mConfig := MIDAConfig{
		NumCrawlers:      3,
		NumStorers:       2,
		UseAMPQForTasks:  false,
		TaskLocation:     "examples/exampleTask.json",
		EnableMonitoring: true,
		PrometheusPort:   DefaultPrometheusPort,
	}

	// Create channels for the pipeline
	monitoringChan := make(chan TaskStats)
	finalResultChan := make(chan FinalMIDAResult)
	rawResultChan := make(chan RawMIDAResult)
	sanitizedTaskChan := make(chan SanitizedMIDATask)
	rawTaskChan := make(chan MIDATask)
	retryChan := make(chan SanitizedMIDATask)

	var crawlerWG sync.WaitGroup  // Tracks active crawler workers
	var storageWG sync.WaitGroup  // Tracks active storage workers
	var pipelineWG sync.WaitGroup // Tracks tasks currently in pipeline

	// Start goroutine that runs the Prometheus monitoring HTTP server
	if mConfig.EnableMonitoring {
		go RunPrometheusClient(monitoringChan, mConfig.PrometheusPort)
	}

	// Start goroutine(s) that handles crawl results storage

	storageWG.Add(mConfig.NumStorers)
	for i := 0; i < mConfig.NumStorers; i++ {
		go StoreResults(finalResultChan, mConfig, monitoringChan, retryChan, &storageWG, &pipelineWG)
	}

	// Start goroutine that handles crawl results sanitization
	go PostprocessResult(rawResultChan, finalResultChan)

	// Start crawler(s) which take sanitized tasks as arguments
	crawlerWG.Add(mConfig.NumCrawlers)
	for i := 0; i < mConfig.NumCrawlers; i++ {
		go CrawlerInstance(sanitizedTaskChan, rawResultChan, retryChan, mConfig, &crawlerWG)
	}

	// Start goroutine which sanitizes input tasks
	go SanitizeTasks(rawTaskChan, sanitizedTaskChan, mConfig, &pipelineWG)

	go TaskIntake(rawTaskChan, mConfig)

	// Once all crawlers have completed, we can close the Raw Result Channel
	crawlerWG.Wait()
	close(rawResultChan)

	// We are done when all storage has completed
	storageWG.Wait()

	// Cleanup remaining artifacts
	err := os.RemoveAll(TempDirectory)
	if err != nil {
		log.Warn(err)
	}

	return

}