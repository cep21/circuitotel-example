/*
Run this simple Go program to see what circuits look like.  You can explore their hystrix stream and expvar values.
*/
package main

import (
	"context"
	"errors"
	"expvar"
	"flag"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/cep21/circuit/v4"
	"github.com/cep21/circuit/v4/closers/hystrix"
	"github.com/cep21/circuitotel"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

func main() {
	interval := flag.Duration("interval", time.Millisecond*1000, "Setup duration between metric ticks")
	flag.StringVar(&collectorEndpoint, "collector", "", "Collector endpoint")
	flag.Parse()
	ctx := context.Background()
	serviceName := "circuit"
	serviceVersion := "0.0.1"
	otelShutdown, err := setupOTelSDK(ctx, serviceName, serviceVersion)
	if err != nil {
		log.Panicf("error setting up OpenTelemetry: %v", err)
		return
	}
	fact := &circuitotel.Factory{
		MeterProvider: otel.GetMeterProvider(),
	}
	h := circuit.Manager{
		DefaultCircuitProperties: []circuit.CommandPropertiesConstructor{
			fact.CommandPropertiesConstructor,
		},
	}
	expvar.Publish("hystrix", h.Var())

	defer func() {
		if err := otelShutdown(ctx); err != nil {
			log.Panicf("error shutting down OpenTelemetry: %v", err)
		}
	}()
	onKill := make(chan os.Signal, 1)
	signal.Notify(onKill, os.Interrupt)
	r := &runWith{
		tp:   otel.GetTracerProvider().Tracer("circuit"),
		name: "circuit",
	}
	createBackgroundCircuits(ctx, r, &h, *interval)
	<-onKill
	log.Println("Shutting down")
	os.Exit(0)
}

type runWith struct {
	tp   trace.Tracer
	name string
}

func (r *runWith) run(ctx context.Context, f func(ctx context.Context) error) error {
	ctx, span := r.tp.Start(ctx, r.name)
	defer span.End()
	return f(ctx)
}

func panicIfNoErr(err error) {
	if err == nil {
		panic("Expected a failure")
	}
}

func panicIfErr(err error) {
	if err != nil {
		panic(err)
	}
}

func setupAlwaysFails(ctx context.Context, r *runWith, h *circuit.Manager, tickInterval time.Duration) {
	failureCircuit := h.MustCreateCircuit("always-fails", circuit.Config{})
	go func() {
		for range time.Tick(tickInterval) {
			panicIfNoErr(r.run(ctx, func(ctx context.Context) error {
				return failureCircuit.Execute(ctx, func(ctx context.Context) error {
					return errors.New("a failure")
				}, nil)
			}))
		}
	}()
}

func setupBadRequest(ctx context.Context, r *runWith, h *circuit.Manager, tickInterval time.Duration) {
	failingBadRequest := h.MustCreateCircuit("always-fails-bad-request", circuit.Config{})
	go func() {
		for range time.Tick(tickInterval) {
			panicIfNoErr(r.run(ctx, func(ctx context.Context) error {
				return failingBadRequest.Execute(ctx, func(ctx context.Context) error {
					return circuit.SimpleBadRequest{Err: errors.New("bad user input")}
				}, nil)
			}))
		}
	}()
}

func setupFailsOriginalContext(ctx context.Context, r *runWith, h *circuit.Manager, tickInterval time.Duration) {
	failingOriginalContextCanceled := h.MustCreateCircuit("always-fails-original-context", circuit.Config{})
	go func() {
		for range time.Tick(tickInterval) {
			endedContext, cancel := context.WithCancel(ctx)
			cancel()
			panicIfNoErr(r.run(ctx, func(ctx context.Context) error {
				return failingOriginalContextCanceled.Execute(endedContext, func(ctx context.Context) error {
					return errors.New("a failure, but it's not my fault")
				}, nil)
			}))
		}
	}()
}

func setupAlwaysPasses(ctx context.Context, r *runWith, h *circuit.Manager, tickInterval time.Duration) {
	passingCircuit := h.MustCreateCircuit("always-passes", circuit.Config{})
	go func() {
		for range time.Tick(tickInterval) {
			panicIfErr(r.run(ctx, func(ctx context.Context) error {
				return passingCircuit.Execute(ctx, func(ctx context.Context) error {
					return nil
				}, nil)
			}))
		}
	}()
}

func setupTimesOut(ctx context.Context, r *runWith, h *circuit.Manager, tickInterval time.Duration) {
	timeOutCircuit := h.MustCreateCircuit("always-times-out", circuit.Config{
		Execution: circuit.ExecutionConfig{
			Timeout: time.Millisecond,
		},
	})
	go func() {
		for range time.Tick(tickInterval) {
			panicIfNoErr(r.run(ctx, func(ctx context.Context) error {
				return timeOutCircuit.Execute(ctx, func(ctx context.Context) error {
					<-ctx.Done()
					return ctx.Err()
				}, nil)
			}))
		}
	}()
}

func setupFallsBack(ctx context.Context, r *runWith, h *circuit.Manager, tickInterval time.Duration) {
	fallbackCircuit := h.MustCreateCircuit("always-falls-back", circuit.Config{
		Execution: circuit.ExecutionConfig{
			Timeout: time.Millisecond,
		},
	})
	go func() {
		for range time.Tick(tickInterval) {
			panicIfErr(r.run(ctx, func(ctx context.Context) error {
				return fallbackCircuit.Execute(ctx, func(ctx context.Context) error {
					return errors.New("a failure")
				}, func(ctx context.Context, err error) error {
					return nil
				})
			}))
		}
	}()
}

func setupRandomExecutionTime(ctx context.Context, r *runWith, h *circuit.Manager, tickInterval time.Duration) {
	randomExecutionTime := h.MustCreateCircuit("random-execution-time", circuit.Config{
		Execution: circuit.ExecutionConfig{},
	})
	go func() {
		for range time.Tick(tickInterval) {
			panicIfErr(r.run(ctx, func(ctx context.Context) error {
				return randomExecutionTime.Execute(ctx, func(ctx context.Context) error {
					select {
					// Some time between 0 and 50ms
					case <-time.After(time.Duration(int64(float64(time.Millisecond.Nanoseconds()*50) * rand.Float64()))):
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}, func(ctx context.Context, err error) error {
					return nil
				})
			}))
		}
	}()
}

func setupFloppyCircuit(ctx context.Context, r *runWith, h *circuit.Manager, tickInterval time.Duration) {
	// Flop every 3 seconds, try to recover very quickly
	floppyCircuit := h.MustCreateCircuit("floppy-circuit", circuit.Config{
		General: circuit.GeneralConfig{
			OpenToClosedFactory: hystrix.CloserFactory(hystrix.ConfigureCloser{
				//		// This should allow a new request every 10 milliseconds
				SleepWindow: time.Millisecond * 10,
			}),
			ClosedToOpenFactory: hystrix.OpenerFactory(hystrix.ConfigureOpener{
				RequestVolumeThreshold: 2,
			}),
		},
	})
	floppyCircuitPasses := int64(1)
	go func() {
		isPassing := true
		for range time.Tick(time.Second * 3) {
			if isPassing {
				atomic.StoreInt64(&floppyCircuitPasses, 0)
			} else {
				atomic.StoreInt64(&floppyCircuitPasses, 1)
			}
			isPassing = !isPassing
		}
	}()
	for i := 0; i < 10; i++ {
		go func() {
			totalErrors := 0
			for range time.Tick(tickInterval) {
				// Errors flop back and forth
				err := r.run(ctx, func(ctx context.Context) error {
					return floppyCircuit.Execute(ctx, func(ctx context.Context) error {
						if atomic.LoadInt64(&floppyCircuitPasses) == 1 {
							return nil
						}
						return errors.New("i'm failing now")
					}, func(ctx context.Context, err error) error {
						return nil
					})
				})
				if err != nil {
					totalErrors++
				}
			}
		}()
	}
}

func setupThrottledCircuit(ctx context.Context, r *runWith, h *circuit.Manager, tickInterval time.Duration) {
	throttledCircuit := h.MustCreateCircuit("throttled-circuit", circuit.Config{
		Execution: circuit.ExecutionConfig{
			MaxConcurrentRequests: 2,
		},
	})
	// 100 threads, every 100ms, someone will get throttled
	for i := 0; i < 100; i++ {
		go func() {
			totalErrors := 0
			for range time.Tick(tickInterval) {
				// Some pass (not throttled) and some don't (throttled)
				err := r.run(ctx, func(ctx context.Context) error {
					return throttledCircuit.Execute(ctx, func(ctx context.Context) error {
						select {
						// Some time between 0 and 50ms
						case <-time.After(time.Duration(int64(float64(time.Millisecond.Nanoseconds()*50) * rand.Float64()))):
							return nil
						case <-ctx.Done():
							return ctx.Err()
						}
					}, nil)
				})
				if err != nil {
					totalErrors++
				}
			}
		}()
	}
}

func createBackgroundCircuits(ctx context.Context, r *runWith, h *circuit.Manager, tickInterval time.Duration) {
	setupAlwaysFails(ctx, r, h, tickInterval)
	setupBadRequest(ctx, r, h, tickInterval)
	setupFailsOriginalContext(ctx, r, h, tickInterval)
	setupAlwaysPasses(ctx, r, h, tickInterval)
	setupTimesOut(ctx, r, h, tickInterval)
	setupFallsBack(ctx, r, h, tickInterval)
	setupRandomExecutionTime(ctx, r, h, tickInterval)
	setupFloppyCircuit(ctx, r, h, tickInterval)
	setupThrottledCircuit(ctx, r, h, tickInterval)
}
