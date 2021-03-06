package exec

import (
    "time"
    "github.com/rwn3120/go-logger"
)

type Status int
type Signal int

const (
    Alive  Status = 1
    Zombie Status = 2

    KillSignal Signal = 9
)

type worker struct {
    uuid      string
    heartbeat time.Duration
    jobs      chan *job
    signals   chan Signal
    done      chan bool
    status    Status
    processor Processor
    logger    *logger.Logger
}

func newWorker(uuid string, heartbeat time.Duration, logging *logger.Configuration, jobs chan *job, factory ProcessorFactory) (*worker, error) {
    logger, err := logger.New(uuid, logging)
    if err != nil {
        return nil, err
    }
    worker := &worker{
        uuid:      uuid,
        heartbeat: heartbeat,
        jobs:      jobs,
        signals:   make(chan Signal, 1),
        done:      make(chan bool, 1),
        status:    Alive,
        processor: factory.Processor(),
        logger:    logger}
    go worker.run()
    return worker, nil
}

func (w *worker) isAlive() bool {
    return w.status == Alive
}

func (w *worker) kill() {
    w.logger.Trace("Sending kill signal to worker %s...", w.uuid)
    w.signals <- KillSignal
}

func (w *worker) wait() bool {
    return <-w.done
}

func (w *worker) die() {
    if w.isAlive() {
        defer w.processor.Destroy()
        w.logger.Trace("Dying...")
        <-time.After(time.Second)
        w.status = Zombie
        w.done <- true
        close(w.done)
        w.logger.Trace("Become a zombie...")
    }
}

func (w *worker) run() {
    defer w.die()
    if err := w.processor.Initialize(); err != nil {
        w.logger.Error("Could not initialize worker: %s", err.Error())

    }

runLoop:
    for counter := 0; w.isAlive(); {
        select {
        // process signals
        case signal := <-w.signals:
            w.logger.Trace("Handling signal %d", signal)
            switch signal {
            case KillSignal:
                w.logger.Trace("Killed")
                break runLoop
            default:
                w.logger.Warn("Unknown signal (%d) received", signal)
            }

            // process jobs
        case job, more := <-w.jobs:
            if more {
                counter++
                w.logger.Trace("Received job %v #%06d", job.correlationId, counter)
                result := w.processor.Process(job.payload)
                w.logger.Trace("Reporting result of job %v #%06d", job.correlationId, counter)
                job.output <- newOutput(job.correlationId, result)
            } else {
                w.logger.Trace("Received all jobs")
                break runLoop
            }
            counter++
        case <-time.After(w.heartbeat):
            w.logger.Trace("Nothing to do")
        }
    }
    w.logger.Trace("Finished")
}
