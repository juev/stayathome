package main

import (
	"context"
	"expvar"
	_ "expvar"
	"net"
	"net/http"

	// "net/http/httptrace"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	httptrace "gopkg.in/DataDog/dd-trace-go.v1/contrib/net/http"

	"github.com/DataDog/datadog-go/statsd"
	"github.com/gorilla/mux"
	"gopkg.in/DataDog/dd-trace-go.v1/ddtrace/tracer"

	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()
	sugar := logger.Sugar().Named("juev")
	sugar.Info("Application is starting...")
	port := os.Getenv("PORT")
	if port == "" {
		sugar.Fatal("Port is not defined")
	}

	diagPort := os.Getenv("DIAG_PORT")
	if diagPort == "" {
		sugar.Fatal("Port is not defined")
	}

	c, err := statsd.New("127.0.0.1:8125")
	if err != nil {
		sugar.Fatalw("Cannot conect to statsd", err)
	}
	c.Namespace = "juevapp."

	tracer.Start(tracer.WithEnv("juev"), tracer.WithAnalytics(true))
	defer tracer.Stop()
	sugar.Infow("tracer was started")
	r := mux.NewRouter()
	server := http.Server{
		Addr:    net.JoinHostPort("", port),
		Handler: r,
	}

	diagLogger := sugar.With("subapp", "diag_router")
	diagRouter := httptrace.NewServeMux()
	diagRouter.HandleFunc("/debug/vars", expvar.Handler().ServeHTTP)
	diagRouter.HandleFunc("/gc", func(
		w http.ResponseWriter, _ *http.Request) {
		diagLogger.Info("Call GC")
		runtime.GC()
		w.WriteHeader(http.StatusOK)
	})
	diagRouter.HandleFunc("/health", func(
		w http.ResponseWriter, _ *http.Request) {
		err := c.Incr("health_calls", []string{}, 1)
		if err != nil {
			diagLogger.Errorw("Cannot inc health")
		}
		diagLogger.Info("Health was called")
		w.WriteHeader(http.StatusOK)
	})

	diag := http.Server{
		Addr:    net.JoinHostPort("", diagPort),
		Handler: diagRouter,
	}

	shutdown := make(chan error, 2)

	sugar.Infof("Business logic server is starting...")
	go func() {
		err := server.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			shutdown <- err
		}
	}()

	sugar.Infof("Diagnostics server is starting...")
	go func() {
		err := diag.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			shutdown <- err
		}
	}()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)

	select {
	case x := <-interrupt:
		sugar.Infow("Recived", "signal", x.String())

	case err := <-shutdown:
		sugar.Errorw("Error from", "err", err)
	}

	timeout, cancelFunc := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFunc()

	err = server.Shutdown(timeout)
	if err != nil {
		sugar.Errorw("The bussines logic is stoped with error", "err", err)
	}

	err = diag.Shutdown(timeout)
	if err != nil {
		sugar.Errorw("The diagnostic logic is stoped with error", "err", err)
	}

	sugar.Info("Application is closed...")
}
