package station

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/wonli/aqi/ws"
)

// transcodeJob is a short-lived in-memory job record. The rendered file is
// still the durable source of truth; the record only lets a connected device
// observe the state before it starts the HTTP media request.
type transcodeJob struct {
	JobID        string
	ServerFileID string
	Profile      string
	Status       string
	Error        string
	Clients      map[*ws.Client]struct{}
}

type transcodePrepareRequest struct {
	ServerFileID string `json:"serverFileId"`
	Profile      string `json:"profile"`
}

type transcodeJobView struct {
	JobID        string `json:"jobId"`
	ServerFileID string `json:"serverFileId"`
	Profile      string `json:"profile"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}

const transcodeJobAction = "v1.station.transcode.progress"

// PrepareTranscode starts or joins the server-side render. It intentionally
// returns immediately; the native client continues to use the normal HTTP
// URL only after receiving ready over this control WebSocket.
func (s *Service) PrepareTranscode(a *ws.Context) {
	if clientDeviceID(a) == "" {
		a.SendCode(401, "device hello required")
		return
	}
	var request transcodePrepareRequest
	if !decodeParams(a, &request) || strings.TrimSpace(request.ServerFileID) == "" ||
		strings.TrimSpace(request.Profile) == "" {
		a.SendCode(400, "invalid transcode request")
		return
	}
	request.ServerFileID = strings.TrimSpace(request.ServerFileID)
	request.Profile = strings.ToLower(strings.TrimSpace(request.Profile))
	if _, err := s.GetServerFile(request.ServerFileID); err != nil {
		a.SendCode(404, "file not found")
		return
	}
	if _, err := s.transcodeProfile(request.Profile); err != nil {
		a.SendCode(422, err.Error())
		return
	}

	jobID := request.ServerFileID + ":" + request.Profile
	s.transcodeJobsMu.Lock()
	job, exists := s.transcodeJobs[jobID]
	if exists && job.Status == "failed" {
		// A failed attempt is not a durable result. Let the next explicit retry
		// create a fresh job so transient FFmpeg, storage, or source errors can
		// recover without waiting for the old status record to expire.
		exists = false
		job = nil
	}
	if !exists {
		job = &transcodeJob{
			JobID: jobID, ServerFileID: request.ServerFileID,
			Profile: request.Profile, Status: "queued",
			Clients: make(map[*ws.Client]struct{}),
		}
		s.transcodeJobs[jobID] = job
	}
	job.Clients[a.Client] = struct{}{}
	view := transcodeJobViewFrom(job)
	s.transcodeJobsMu.Unlock()
	if !exists {
		go s.runTranscodeJob(job)
	}
	a.Send(view)
}

func transcodeJobViewFrom(job *transcodeJob) transcodeJobView {
	return transcodeJobView{
		JobID: job.JobID, ServerFileID: job.ServerFileID,
		Profile: job.Profile, Status: job.Status, Error: job.Error,
	}
}

func (s *Service) runTranscodeJob(job *transcodeJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	s.updateTranscodeJob(job, "transcoding", "")
	var err error
	for {
		var rendered *os.File
		rendered, err = s.OpenWebTranscode(ctx, job.ServerFileID, job.Profile)
		if rendered != nil {
			_ = rendered.Close()
		}
		if !errors.Is(err, ErrTranscodeBusy) || ctx.Err() != nil {
			break
		}
		// A full slot is queueing, not a failed conversion. Avoid making the
		// player retry the same request while another rendition is finishing.
		s.updateTranscodeJob(job, "queued", "")
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(2 * time.Second):
			s.updateTranscodeJob(job, "transcoding", "")
		}
	}
	if err != nil {
		s.updateTranscodeJob(job, "failed", err.Error())
	} else {
		s.updateTranscodeJob(job, "ready", "")
	}

	// Keep the map bounded. The rendered file remains available to the HTTP
	// endpoint, while a later request can create a fresh status record.
	time.AfterFunc(10*time.Minute, func() {
		s.transcodeJobsMu.Lock()
		if current, ok := s.transcodeJobs[job.JobID]; ok && current == job {
			delete(s.transcodeJobs, job.JobID)
		}
		s.transcodeJobsMu.Unlock()
	})
}

func (s *Service) updateTranscodeJob(job *transcodeJob, status, message string) {
	s.transcodeJobsMu.Lock()
	job.Status = status
	job.Error = message
	view := transcodeJobViewFrom(job)
	clients := make([]*ws.Client, 0, len(job.Clients))
	for client := range job.Clients {
		clients = append(clients, client)
	}
	if status == "ready" || status == "failed" {
		job.Clients = make(map[*ws.Client]struct{})
	}
	s.transcodeJobsMu.Unlock()

	messageAction := &ws.Action{Action: transcodeJobAction, Data: view}
	for _, client := range clients {
		if client != nil {
			client.SendActionMsg(messageAction)
		}
	}
}
