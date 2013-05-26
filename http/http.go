package rafthttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/peterbourgon/raft"
	"io/ioutil"
	"net/http"
	"net/url"
)

const (
	IdPath            = "/raft/id"
	AppendEntriesPath = "/raft/appendentries"
	RequestVotePath   = "/raft/requestvote"
	CommandPath       = "/raft/command"
)

var (
	emptyAppendEntriesResponse bytes.Buffer
	emptyRequestVoteResponse   bytes.Buffer
)

func init() {
	json.NewEncoder(&emptyAppendEntriesResponse).Encode(raft.AppendEntriesResponse{})
	json.NewEncoder(&emptyRequestVoteResponse).Encode(raft.RequestVoteResponse{})
}

type HTTPPeer struct {
	id  uint64
	url url.URL
}

func NewHTTPPeer(u url.URL) (*HTTPPeer, error) {
	u.Path = ""

	idUrl := u
	idUrl.Path = IdPath
	resp, err := http.Get(idUrl.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var id uint64
	if err := json.NewDecoder(resp.Body).Decode(&id); err != nil {
		return nil, err
	}

	return &HTTPPeer{
		id:  id,
		url: u,
	}, nil
}

func (p *HTTPPeer) Id() uint64 { return p.id }

func (p *HTTPPeer) AppendEntries(ae raft.AppendEntries) raft.AppendEntriesResponse {
	var aer raft.AppendEntriesResponse
	p.rpc(ae, AppendEntriesPath, &aer)
	return aer
}

func (p *HTTPPeer) RequestVote(rv raft.RequestVote) raft.RequestVoteResponse {
	var rvr raft.RequestVoteResponse
	p.rpc(rv, RequestVotePath, &rvr)
	return rvr
}

func (p *HTTPPeer) Command(cmd []byte, response chan []byte) error {
	go func() {
		var responseBuf bytes.Buffer
		p.rpc(cmd, CommandPath, &responseBuf)
		response <- responseBuf.Bytes()
	}()
	return nil // TODO could make this smarter (i.e. timeout), with more work
}

func (p *HTTPPeer) rpc(request interface{}, path string, response interface{}) error {
	body := &bytes.Buffer{}
	if err := json.NewEncoder(body).Encode(request); err != nil {
		return err
	}

	url := p.url
	url.Path = path
	resp, err := http.Post(url.String(), "application/json", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
		return err
	}

	return nil
}

type HTTPServer struct {
	server *raft.Server
}

func NewHTTPServer(server *raft.Server) *HTTPServer {
	return &HTTPServer{
		server: server,
	}
}

type Muxer interface {
	HandleFunc(string, http.HandlerFunc)
}

func (s *HTTPServer) Install(mux Muxer) {
	mux.HandleFunc(IdPath, s.idHandler())
	mux.HandleFunc(AppendEntriesPath, s.appendEntriesHandler())
	mux.HandleFunc(RequestVotePath, s.requestVoteHandler())
	mux.HandleFunc(CommandPath, s.commandHandler())
}

func (s *HTTPServer) idHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fmt.Sprint(s.server.Id())))
	}
}

func (s *HTTPServer) appendEntriesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var ae raft.AppendEntries
		if err := json.NewDecoder(r.Body).Decode(&ae); err != nil {
			http.Error(w, emptyAppendEntriesResponse.String(), http.StatusBadRequest)
			return
		}

		aer := s.server.AppendEntries(ae)
		if err := json.NewEncoder(w).Encode(aer); err != nil {
			http.Error(w, emptyAppendEntriesResponse.String(), http.StatusInternalServerError)
			return
		}
	}
}

func (s *HTTPServer) requestVoteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var rv raft.RequestVote
		if err := json.NewDecoder(r.Body).Decode(&rv); err != nil {
			http.Error(w, emptyRequestVoteResponse.String(), http.StatusBadRequest)
			return
		}

		rvr := s.server.RequestVote(rv)
		if err := json.NewEncoder(w).Encode(rvr); err != nil {
			http.Error(w, emptyRequestVoteResponse.String(), http.StatusInternalServerError)
			return
		}
	}
}

func (s *HTTPServer) commandHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		// TODO unfortunately, we squelch a lot of errors here.
		// Maybe there's a way to report different classes of errors
		// than with an empty response.

		cmd, err := ioutil.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "", http.StatusBadRequest)
			return
		}

		response := make(chan []byte, 1)
		if err := s.server.Command(cmd, response); err != nil {
			http.Error(w, "", http.StatusInternalServerError)
			return
		}

		resp, ok := <-response
		if !ok {
			http.Error(w, "", http.StatusInternalServerError)
			return
		}

		w.Write(resp)
	}
}
