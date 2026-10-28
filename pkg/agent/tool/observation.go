package tool

import (
	"loop/pkg/domain/observation"
	domaintool "loop/pkg/domain/tool"
)

type FileObservation = observation.FileObservation
type FileObservationTracker = observation.FileObservationTracker
type FileObserver = domaintool.FileObserver

var NewFileObservationTracker = observation.NewFileObservationTracker
