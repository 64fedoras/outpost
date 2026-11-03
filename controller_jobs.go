package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// dispatchJob sends a job to a node and records it for the UI. The returned
// channel receives the result once (or a "lost"/"failed" result if the node
// disappears).
func (c *Controller) dispatchJob(nodeID string, job *JobDispatch, command string) (<-chan *JobResult, error) {
	c.mu.Lock()
	n, ok := c.nodes[nodeID]
	nc := c.conns[nodeID]
	if !ok || nc == nil {
		c.mu.Unlock()
		return nil, errors.New("node is offline")
	}
	if job.JobID == "" {
		job.JobID = newID("job")
	}
	rec := &JobRec{ID: job.JobID, NodeID: nodeID, NodeName: displayName(n), Kind: job.Kind, Command: command, Status: "dispatched", Created: time.Now()}
	c.addJobLocked(rec)
	ch := make(chan *JobResult, 1)
	c.waiters[job.JobID] = ch
	c.mu.Unlock()
	if err := nc.send("job.dispatch", job); err != nil {
		c.mu.Lock()
		delete(c.waiters, job.JobID)
		c.finishJobLocked(rec, "failed", errf("send_failed", true, "%v", err))
		c.mu.Unlock()
		return nil, err
	}
	return ch, nil
}

// notifyWaiterLocked delivers a job's result to whoever dispatched it.
func (c *Controller) notifyWaiterLocked(j *JobRec) {
	ch, ok := c.waiters[j.ID]
	if !ok {
		return
	}
	delete(c.waiters, j.ID)
	res := j.Result
	if res == nil {
		res = &JobResult{JobID: j.ID, Status: j.Status}
	}
	ch <- res
}

// runInferenceJob runs one chat completion on a node and waits for the answer.
func (c *Controller) runInferenceJob(ctx context.Context, nodeID string, spec InferenceSpec, label string, timeout time.Duration) (*InferenceOutput, error) {
	return c.runInferenceJobLive(ctx, nodeID, spec, label, timeout, nil)
}

// runInferenceJobLive is runInferenceJob with the streamed text also going to a live handle.
func (c *Controller) runInferenceJobLive(ctx context.Context, nodeID string, spec InferenceSpec, label string, timeout time.Duration, live *liveHandle) (*InferenceOutput, error) {
	spec.Stream = true
	b, _ := json.Marshal(spec)
	job := &JobDispatch{Kind: "inference", TimeoutS: int(timeout.Seconds()), Spec: b}
	if live != nil {
		job.JobID = newID("job")
		live.bindJob(job.JobID)
	}
	ch, err := c.dispatchJob(nodeID, job, label)
	if err != nil {
		return nil, err
	}
	select {
	case res := <-ch:
		if res.Status != "succeeded" {
			if res.Error != nil {
				return nil, res.Error
			}
			return nil, errors.New("inference " + res.Status)
		}
		var out InferenceOutput
		if err := json.Unmarshal(res.Output, &out); err != nil {
			return nil, err
		}
		return &out, nil
	case <-ctx.Done():
		c.cancelJob(job.JobID)
		return nil, ctx.Err()
	}
}

// runExecJob runs a command on a node with the given input files.
func (c *Controller) runExecJob(ctx context.Context, nodeID string, argv []string, inputs []FileEntry, label string, timeout time.Duration) (*ExecOutput, string, error) {
	spec, _ := json.Marshal(ExecSpec{Argv: argv, Stream: true, Network: execNetwork(argv)})
	job := &JobDispatch{Kind: "exec", TimeoutS: int(timeout.Seconds()), Inputs: inputs, Spec: spec}
	ch, err := c.dispatchJob(nodeID, job, label)
	if err != nil {
		return nil, "", err
	}
	select {
	case res := <-ch:
		if res.Status != "succeeded" {
			if res.Error != nil {
				return nil, res.Status, res.Error
			}
			return nil, res.Status, errors.New("job " + res.Status)
		}
		var out ExecOutput
		_ = json.Unmarshal(res.Output, &out)
		return &out, res.Status, nil
	case <-ctx.Done():
		c.cancelJob(job.JobID)
		return nil, "cancelled", ctx.Err()
	}
}

func (c *Controller) cancelJob(jobID string) {
	c.mu.Lock()
	j := c.jobs[jobID]
	var nc *nodeConn
	if j != nil {
		nc = c.conns[j.NodeID]
	}
	c.mu.Unlock()
	if nc != nil {
		_ = nc.send("job.cancel", JobCancel{JobID: jobID, Reason: "task cancelled"})
	}
}

func inlineFile(path string, data []byte) FileEntry {
	sum := sha256.Sum256(data)
	return FileEntry{Path: path, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data)), Mode: "0644", InlineB64: base64.StdEncoding.EncodeToString(data)}
}
