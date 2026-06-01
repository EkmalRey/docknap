package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStatusRecorderDefaultStatusOnWrite(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: rr, status: http.StatusBadGateway}

	if _, err := rec.Write([]byte("hello")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if rec.status != http.StatusOK {
		t.Errorf("Write without WriteHeader should set status to 200, got %d", rec.status)
	}
	if !rec.wroteHeader {
		t.Errorf("Write should mark wroteHeader=true")
	}
	if rr.Code != http.StatusOK {
		t.Errorf("underlying response should be 200, got %d", rr.Code)
	}
}

func TestStatusRecorderExplicitWriteHeader(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: rr, status: http.StatusBadGateway}

	rec.WriteHeader(http.StatusTeapot)
	if rec.status != http.StatusTeapot {
		t.Errorf("WriteHeader should set status, got %d", rec.status)
	}
	if _, err := rec.Write([]byte("hello")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if rec.status != http.StatusTeapot {
		t.Errorf("Write after WriteHeader should not change status, got %d", rec.status)
	}
}

func TestStatusRecorderDoubleWriteHeader(t *testing.T) {
	rr := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: rr, status: http.StatusBadGateway}

	rec.WriteHeader(http.StatusTeapot)
	rec.WriteHeader(http.StatusOK)
	if rec.status != http.StatusTeapot {
		t.Errorf("second WriteHeader should be ignored, got %d", rec.status)
	}
}
