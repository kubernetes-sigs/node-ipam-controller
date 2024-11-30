package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	utilwait "k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

const defaultTimeout = 5 * time.Second

// StartHealthProbeServer starts a web server that has two endpoints `/readyz` and `/healthz` and always responds
// 200 OK.
func StartHealthProbeServer(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.Handle("/readyz", makeHealthHandler())
	mux.Handle("/healthz", makeHealthHandler())
	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  defaultTimeout,
		WriteTimeout: defaultTimeout,
		IdleTimeout:  defaultTimeout,
	}

	klog.Infof("Starting health server at %s", addr)

	go func() {
		go utilwait.Until(func() {
			err := server.ListenAndServe()
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				utilruntime.HandleError(fmt.Errorf("starting health server failed: %w", err))
			}
		}, defaultTimeout, ctx.Done())

		<-ctx.Done()

		klog.Infof("Stopping health server %s", server.Addr)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			klog.Errorf("Error stopping health server: %v", err)
		}
	}()
}

// makeHealthHandler returns 200/OK when healthy.
func makeHealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		w.WriteHeader(http.StatusOK)
	}
}

// StartMetricsServer starts a web server with `/metrics` endpoint.
func StartMetricsServer(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  defaultTimeout,
		WriteTimeout: defaultTimeout,
		IdleTimeout:  defaultTimeout,
	}

	klog.Infof("Starting metrics server at %s", addr)

	go func() {
		go utilwait.Until(func() {
			err := server.ListenAndServe()
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				utilruntime.HandleError(fmt.Errorf("starting metrics server failed: %w", err))
			}
		}, defaultTimeout, ctx.Done())

		<-ctx.Done()
		klog.Infof("Stopping metrics server %s", server.Addr)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			klog.Errorf("Error stopping metrics server: %v", err)
		}
	}()
}
