package exec

import (
    "fmt"
    "time"
    "github.com/rwn3120/go-conf"
    "github.com/rwn3120/go-logger"
    "github.com/pkg/errors"
)

type Executor struct {
    name          string
    configuration *Configuration
    jobs          chan *job
    workers       map[string]*worker
    logger        *logger.Logger
}

func New(name string, configuration *Configuration, factory ProcessorFactory) (*Executor, error) {
    conf.Check(configuration)
    logger, err := logger.New(name, configuration.Logger)
    if err != nil {
        return nil, err
    }
    // create executor
    executor := &Executor{
        name:          name,
        configuration: configuration,
        jobs:          make(chan *job, configuration.Workers),
        workers:       make(map[string]*worker, configuration.Workers),
        logger:        logger}
    // start workers
    for index := 0; index < executor.configuration.Workers; index++ {
        workerUuid := fmt.Sprintf("%s-worker-%d", name, index+1)
        executor.logger.Trace("Creating worker %s (%d/%d)", workerUuid, index+1, configuration.Workers)
        worker, err := newWorker(
            workerUuid,
            executor.configuration.Heartbeat,
            executor.configuration.Logger,
            executor.jobs,
            factory)
        if err != nil {
            return nil, err
        }
        executor.logger.Trace("Registering worker %s", workerUuid)
        executor.workers[worker.uuid] = worker
    }
    return executor, nil
}

func (e *Executor) Destroy() {
    unregisteredWorkers := make(chan string, len(e.workers))
    workersCount := len(e.workers)
    // kill workers
    for _, w := range e.workers {
        e.logger.Trace("Waiting for worker %s...", w.uuid)
        go func(worker *worker) {
            e.logger.Trace("Killing worker %s...", worker.uuid)
            worker.kill()
            e.logger.Trace("Waiting for worker's %s death...", worker.uuid)
            worker.wait()
            e.logger.Trace("Unregistering worker %s (he is gone)", worker.uuid)
            unregisteredWorkers <- worker.uuid
        }(w)
    }
    // wait for all workers
    for i := 0; i < workersCount; i++ {
        unregisteredWorkerUUid := <-unregisteredWorkers
        e.logger.Trace("Worker %s unregistered", unregisteredWorkerUUid)
        delete(e.workers, unregisteredWorkerUUid)
    }
    e.logger.Trace("Closing jobs channel")
    close(e.jobs)
    e.logger.Trace("Destroyed")
}

func min(first time.Duration, second time.Duration) time.Duration {
    if first == NeverExpires {
        return second
    }
    if second == NeverExpires {
        return first
    }
    if first < second {
        return first
    }
    return second
}

func (e *Executor) processResult(job *job, callbacks ...func(result Result)) {
    for {
        select {
        case result := <-job.output:
            e.logger.Trace("Job %s done. Calling %d callbacks", result.correlationId, len(callbacks))
            go func() {
                for index, callback := range callbacks {
                    if callback != nil {
                        e.logger.Trace("Running callback function (%d/%d)", index+1, len(callbacks))
                        callback(result.result)
                    } else {
                        e.logger.Trace("Skipping callback function (%d/%d)", index+1, len(callbacks))
                    }
                }
            }()
            return
        case <-time.After(e.configuration.Heartbeat):
            e.logger.Trace("Waiting for output of job %s", job.correlationId)
        }
    }
}

func (e *Executor) fire(job *job, callbacks ...func(result Result)) {
    e.logger.Trace("Firing job %s with %d callbacks.", job.correlationId, len(callbacks))
    registeredAt := time.Now()
    for job.expiresAfter == NeverExpires || time.Since(registeredAt) < job.expiresAfter {
        select {
        case e.jobs <- job:
            e.logger.Trace("Job %s has been fired.", job.correlationId)
            e.processResult(job, callbacks...)
            return
        case <-time.After(min(e.configuration.Heartbeat, job.expiresAfter)):
            e.logger.Trace("Job %s has not been fired yet (waiting for free worker).", job.correlationId)
        }
    }
    e.logger.Warn("Job %s has expired", job.correlationId)
    job.output <- newOutput(job.correlationId, &Expired{})
}

// Start job execution and set its callbacks (asynchronous operation)
func (e *Executor) FireJob(payload Payload, callbacks ...func(result Result)) error {
    job, err := newJob(payload)
    if err != nil {
        return err
    }
    go e.fire(job, callbacks...)
    return nil
}

// Start job execution and forget (asynchronous operation)
func (e *Executor) FireAndForgetJob(job Payload) error {
    return e.FireJob(job, func(result Result) {
        // a dummy callback
    })
}

// Execute job (synchronous operation)
func (e *Executor) ExecuteJob(payload Payload) (Result, error) {
    job, err := newJob(payload)
    if err != nil {
        return nil, err
    }
    channel := make(chan Result)
    e.fire(job, func(result Result) {
        channel <- result
    })

    for {
        select {
        case result, more := <-channel:
            if more {
                return result, nil
            } else {
                return nil, errors.New(fmt.Sprintf("Could not get a result of job %s", job.correlationId))
            }

        case <-time.After(e.configuration.Heartbeat):
            e.logger.Trace("Waiting for result of job %s", job.correlationId)
        }
    }
}
