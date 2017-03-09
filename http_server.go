/* Copyright 2015 LinkedIn Corp. Licensed under the Apache License, Version
 * 2.0 (the "License"); you may not use this file except in compliance with
 * the License. You may obtain a copy of the License at
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 */

package main

import (
	"encoding/json"
	"github.com/linkedin/Burrow/protocol"
	"github.com/linkedin/Burrow/security"
	"io"
	"net/http"
	"os"
	"strings"
	"net"
	"time"
	"fmt"
	log "github.com/cihub/seelog"
	"crypto/tls"
)

type HttpServer struct {
	app *ApplicationContext
	mux *http.ServeMux
}

type appHandler struct {
	app     *ApplicationContext
	userChecker *security.UserChecker
	handler func(*ApplicationContext, http.ResponseWriter, *http.Request) int
}


// tcpKeepAliveListener sets TCP keep-alive timeouts on accepted
// connections. It's used by ListenAndServe and ListenAndServeTLS so
// dead TCP connections (e.g. closing laptop mid-download) eventually
// go away.
type tcpKeepAliveListener struct {
	*net.TCPListener
}

func (ln tcpKeepAliveListener) Accept() (c net.Conn, err error) {
	tc, err := ln.AcceptTCP()
	if err != nil {
		return
	}
	tc.SetKeepAlive(true)
	tc.SetKeepAlivePeriod(3 * time.Minute)
	return tc, nil
}

func newHttpListener(listenAddress string, config *BurrowConfig) (net.Listener, error) {
	scheme, host, port := SplitHttpListen(listenAddress)
	listen, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	listen = tcpKeepAliveListener{listen.(*net.TCPListener)}
	if scheme == "https" {
		tlsConfig, err := NewTLSConfig(config, config.Httpserver.TLS)
		if err != nil {
			return nil, err
		}
		listen = tls.NewListener(listen, tlsConfig)
	}
	return listen, err
}

func NewHttpServer(app *ApplicationContext) (*HttpServer, error) {
	server := &HttpServer{
		app: app,
		mux: http.NewServeMux(),
	}

	// This is a catchall for undefined URLs
	server.mux.HandleFunc(server.app.Config.Httpserver.PathPrefix, handleDefault)

	// This is a healthcheck URL. Please don't change it
	server.mux.HandleFunc(server.app.Config.Httpserver.PathPrefix + "burrow/admin", handleAdmin)

	// User checker
	userChecker, err := security.NewUserChecker(app.Config.Httpserver.BasicAuthEnabled,
		app.Config.Httpserver.BasicAuthRealmName,
		app.Config.Httpserver.BasicAuthUserConfigFile,
		app.Config.Httpserver.BasicAuthAnonymousRole)
	if err != nil {
		return nil, err
	}

	// All valid paths go here. Make sure they use the right handler
	server.mux.Handle(server.app.Config.Httpserver.PathPrefix + "v2/kafka", appHandler{server.app, userChecker, handleClusterList})
	server.mux.Handle(server.app.Config.Httpserver.PathPrefix + "v2/kafka/", appHandler{server.app, userChecker, handleKafka})
	server.mux.Handle(server.app.Config.Httpserver.PathPrefix + "v2/zookeeper", appHandler{server.app, userChecker, handleClusterList})
	// server.mux.Handle(server.app.Config.Httpserver.PathPrefix + "/v2/zookeeper/", appHandler{server.app, userChecker, handleZookeeper})

	listeners := make([]net.Listener, 0, len(server.app.Config.Httpserver.Listen))
	for _, listenAddress := range server.app.Config.Httpserver.Listen {
		ln, err := newHttpListener(listenAddress, server.app.Config)
		if err != nil {
			for _, listenerToClose := range listeners {
				closeErr := listenerToClose.Close()
				if closeErr != nil {
					log.Errorf("Could not close listener: %v", closeErr)
				}
			}
			return nil, err
		}
		listeners = append(listeners, ln)
	}

	httpServer := &http.Server{Handler: server.mux}
	for _, listener := range listeners {
		go httpServer.Serve(listener)
	}

	return server, nil
}

func (ah appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if ah.userChecker != nil && ah.userChecker.Enabled {
		username, password, ok := r.BasicAuth()
		var authorized bool
		if ok {
			authorized = ah.userChecker.Check(username, password, r.Method, r.URL.Path)
		} else {
			// Anonymous
			authorized = ah.userChecker.Check("", "", r.Method, r.URL.Path)
		}
		if !authorized {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf("Basic realm=\"%s\"", ah.userChecker.RealmName))
			http.Error(w, "{\"error\":true,\"message\":\"request not authorized\",\"result\":{}}", http.StatusForbidden)
			return
		}
	}
	switch {
	case r.Method == "GET":
		ah.handler(ah.app, w, r)
	case r.Method == "DELETE":
		// Later we can add authentication here
		ah.handler(ah.app, w, r)
	default:
		http.Error(w, "{\"error\":true,\"message\":\"request method not supported\",\"result\":{}}", http.StatusMethodNotAllowed)
	}
}

// This is a catch-all handler for unknown URLs. It should return a 404
func handleDefault(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "{\"error\":true,\"message\":\"invalid request type\",\"result\":{}}", http.StatusNotFound)
}

func handleAdmin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "{\"error\":true,\"message\":\"request method not supported\",\"result\":{}}", http.StatusMethodNotAllowed)
	}
	io.WriteString(w, "GOOD")
}

type HTTPResponseRequestInfo struct {
	URI     string `json:"url"`
	Host    string `json:"host"`
	Cluster string `json:"cluster"`
	Group   string `json:"group"`
	Topic   string `json:"topic"`
}
type HTTPResponseError struct {
	Error   bool                    `json:"error"`
	Message string                  `json:"message"`
	Result  map[string]string       `json:"result"`
	Request HTTPResponseRequestInfo `json:"request"`
}
type HTTPResponseClusterDetailCluster struct {
	Zookeepers    []string `json:"zookeepers"`
	ZookeeperPort int      `json:"zookeeper_port"`
	ZookeeperPath string   `json:"zookeeper_path"`
	Brokers       []string `json:"brokers"`
	BrokerPort    int      `json:"broker_port"`
	OffsetsTopic  string   `json:"offsets_topic"`
}
type HTTPResponseClusterDetail struct {
	Error   bool                             `json:"error"`
	Message string                           `json:"message"`
	Cluster HTTPResponseClusterDetailCluster `json:"cluster"`
	Request HTTPResponseRequestInfo          `json:"request"`
}
type HTTPResponseClusterList struct {
	Error    bool                    `json:"error"`
	Message  string                  `json:"message"`
	Clusters []string                `json:"clusters"`
	Request  HTTPResponseRequestInfo `json:"request"`
}
type HTTPResponseTopicList struct {
	Error   bool                    `json:"error"`
	Message string                  `json:"message"`
	Topics  []string                `json:"topics"`
	Request HTTPResponseRequestInfo `json:"request"`
}
type HTTPResponseTopicDetail struct {
	Error   bool                    `json:"error"`
	Message string                  `json:"message"`
	Offsets []int64                 `json:"offsets"`
	Request HTTPResponseRequestInfo `json:"request"`
}
type HTTPResponseConsumerList struct {
	Error     bool                    `json:"error"`
	Message   string                  `json:"message"`
	Consumers []string                `json:"consumers"`
	Request   HTTPResponseRequestInfo `json:"request"`
}
type HTTPResponseConsumerStatus struct {
	Error   bool                         `json:"error"`
	Message string                       `json:"message"`
	Status  protocol.ConsumerGroupStatus `json:"status"`
	Request HTTPResponseRequestInfo      `json:"request"`
}

func makeRequestInfo(r *http.Request) HTTPResponseRequestInfo {
	hostname, _ := os.Hostname()
	return HTTPResponseRequestInfo{
		URI:  r.URL.Path,
		Host: hostname,
	}
}

func writeJSONResponse(w http.ResponseWriter, status int, value  interface{}) int {
	jsonStr, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "{\"error\":true,\"message\":\"could not encode JSON\",\"result\":{}}", http.StatusInternalServerError)
	}
	w.WriteHeader(status)
	w.Write(jsonStr)
	return status
}

func makeErrorResponse(errValue int, message string, w http.ResponseWriter, r *http.Request) int {
	rv := HTTPResponseError{
		Error:   true,
		Message: message,
		Request: makeRequestInfo(r),
	}

	return writeJSONResponse(w, errValue, rv)
}

func handleClusterList(app *ApplicationContext, w http.ResponseWriter, r *http.Request) int {
	if r.Method != "GET" {
		return makeErrorResponse(http.StatusMethodNotAllowed, "request method not supported", w, r)
	}

	clusterList := make([]string, len(app.Config.Kafka))
	i := 0
	for cluster, _ := range app.Config.Kafka {
		clusterList[i] = cluster
		i++
	}
	requestInfo := makeRequestInfo(r)
	return writeJSONResponse(w, 200, HTTPResponseClusterList{
		Error:    false,
		Message:  "cluster list returned",
		Clusters: clusterList,
		Request:  requestInfo,
	})
}

// This is a router for all requests that operate against Kafka clusters (/v2/kafka/...)
func handleKafka(app *ApplicationContext, w http.ResponseWriter, r *http.Request) int {
	pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, app.Config.Httpserver.PathPrefix), "/")
	if _, ok := app.Config.Kafka[pathParts[2]]; !ok {
		return makeErrorResponse(http.StatusNotFound, "cluster not found", w, r)
	}
	if pathParts[2] == "" {
		// Allow a trailing / on requests
		return handleClusterList(app, w, r)
	}
	if (len(pathParts) == 3) || (pathParts[3] == "") {
		return handleClusterDetail(app, w, r, pathParts[2])
	}

	switch pathParts[3] {
	case "consumer":
		switch {
		case r.Method == "DELETE":
			switch {
			case (len(pathParts) == 5) || (pathParts[5] == ""):
				return handleConsumerDrop(app, w, r, pathParts[2], pathParts[4])
			default:
				return makeErrorResponse(http.StatusMethodNotAllowed, "request method not supported", w, r)
			}
		case r.Method == "GET":
			switch {
			case (len(pathParts) == 4) || (pathParts[4] == ""):
				return handleConsumerList(app, w, r, pathParts[2])
			case (len(pathParts) == 5) || (pathParts[5] == ""):
				// Consumer detail - list of consumer streams/hosts? Can be config info later
				return makeErrorResponse(http.StatusNotFound, "unknown API call", w, r)
			case pathParts[5] == "topic":
				switch {
				case (len(pathParts) == 6) || (pathParts[6] == ""):
					return handleConsumerTopicList(app, w, r, pathParts[2], pathParts[4])
				case (len(pathParts) == 7) || (pathParts[7] == ""):
					return handleConsumerTopicDetail(app, w, r, pathParts[2], pathParts[4], pathParts[6])
				}
			case pathParts[5] == "status":
				return handleConsumerStatus(app, w, r, pathParts[2], pathParts[4], false)
			case pathParts[5] == "lag":
				return handleConsumerStatus(app, w, r, pathParts[2], pathParts[4], true)
			}
		default:
			return makeErrorResponse(http.StatusMethodNotAllowed, "request method not supported", w, r)
		}
	case "topic":
		switch {
		case r.Method != "GET":
			return makeErrorResponse(http.StatusMethodNotAllowed, "request method not supported", w, r)
		case (len(pathParts) == 4) || (pathParts[4] == ""):
			return handleBrokerTopicList(app, w, r, pathParts[2])
		case (len(pathParts) == 5) || (pathParts[5] == ""):
			return handleBrokerTopicDetail(app, w, r, pathParts[2], pathParts[4])
		}
	case "offsets":
		// Reserving this endpoint to implement later
		return makeErrorResponse(http.StatusNotFound, "unknown API call", w, r)
	}

	// If we fell through, return a 404
	return makeErrorResponse(http.StatusNotFound, "unknown API call", w, r)
}

func handleClusterDetail(app *ApplicationContext, w http.ResponseWriter, r *http.Request, cluster string) int {
	// Clearly show the root path in ZK (which we have a blank for after config)
	zkPath := app.Config.Kafka[cluster].ZookeeperPath
	if zkPath == "" {
		zkPath = "/"
	}

	requestInfo := makeRequestInfo(r)
	requestInfo.Cluster = cluster
	return writeJSONResponse(w, 200, HTTPResponseClusterDetail{
		Request: requestInfo,
		Error:   false,
		Message: "cluster detail returned",
		Cluster: HTTPResponseClusterDetailCluster{
			Zookeepers:    app.Config.Kafka[cluster].Zookeepers,
			ZookeeperPort: app.Config.Kafka[cluster].ZookeeperPort,
			ZookeeperPath: zkPath,
			Brokers:       app.Config.Kafka[cluster].Brokers,
			BrokerPort:    app.Config.Kafka[cluster].BrokerPort,
			OffsetsTopic:  app.Config.Kafka[cluster].OffsetsTopic,
		},
	})
}

func handleConsumerList(app *ApplicationContext, w http.ResponseWriter, r *http.Request, cluster string) int {
	storageRequest := &RequestConsumerList{Result: make(chan []string), Cluster: cluster}
	app.Storage.requestChannel <- storageRequest

	requestInfo := makeRequestInfo(r)
	requestInfo.Cluster = cluster
	return writeJSONResponse(w, 200, HTTPResponseConsumerList{
		Error:     false,
		Request:   requestInfo,
		Message:   "consumer list returned",
		Consumers: <-storageRequest.Result,
	})
}

func handleConsumerTopicList(app *ApplicationContext, w http.ResponseWriter, r *http.Request, cluster string, group string) int {
	storageRequest := &RequestTopicList{Result: make(chan *ResponseTopicList), Cluster: cluster, Group: group}
	app.Storage.requestChannel <- storageRequest
	result := <-storageRequest.Result
	if result.Error {
		return makeErrorResponse(http.StatusNotFound, "consumer group not found", w, r)
	}

	requestInfo := makeRequestInfo(r)
	requestInfo.Cluster = cluster
	requestInfo.Group = group
	return writeJSONResponse(w, 200, HTTPResponseTopicList{
		Error:   false,
		Message: "consumer topic list returned",
		Topics:  result.TopicList,
		Request: requestInfo,
	})
}

func handleConsumerTopicDetail(app *ApplicationContext, w http.ResponseWriter, r *http.Request, cluster string, group string, topic string) int {
	storageRequest := &RequestOffsets{Result: make(chan *ResponseOffsets), Cluster: cluster, Topic: topic, Group: group}
	app.Storage.requestChannel <- storageRequest
	result := <-storageRequest.Result
	if result.ErrorGroup {
		return makeErrorResponse(http.StatusNotFound, "consumer group not found", w, r)
	}
	if result.ErrorTopic {
		return makeErrorResponse(http.StatusNotFound, "topic not found for consumer group", w, r)
	}

	requestInfo := makeRequestInfo(r)
	requestInfo.Cluster = cluster
	requestInfo.Group = group
	requestInfo.Topic = topic
	return writeJSONResponse(w, 200, HTTPResponseTopicDetail{
		Error:   false,
		Message: "consumer group topic offsets returned",
		Offsets: result.OffsetList,
		Request: requestInfo,
	})
}

func handleConsumerStatus(app *ApplicationContext, w http.ResponseWriter, r *http.Request, cluster string, group string, showall bool) int {
	storageRequest := &RequestConsumerStatus{Result: make(chan *protocol.ConsumerGroupStatus), Cluster: cluster, Group: group, Showall: showall}
	app.Storage.requestChannel <- storageRequest
	result := <-storageRequest.Result
	if result.Status == protocol.StatusNotFound {
		return makeErrorResponse(http.StatusNotFound, "consumer group not found", w, r)
	}

	requestInfo := makeRequestInfo(r)
	requestInfo.Cluster = cluster
	requestInfo.Group = group
	return writeJSONResponse(w, 200, HTTPResponseConsumerStatus{
		Error:   false,
		Message: "consumer group status returned",
		Status:  *result,
		Request: requestInfo,
	})
}

func handleConsumerDrop(app *ApplicationContext, w http.ResponseWriter, r *http.Request, cluster string, group string) int {
	storageRequest := &RequestConsumerDrop{Result: make(chan protocol.StatusConstant), Cluster: cluster, Group: group}
	app.Storage.requestChannel <- storageRequest
	result := <-storageRequest.Result
	if result == protocol.StatusNotFound {
		return makeErrorResponse(http.StatusNotFound, "consumer group not found", w, r)
	}

	requestInfo := makeRequestInfo(r)
	requestInfo.Cluster = cluster
	requestInfo.Group = group
	return writeJSONResponse(w, 200, HTTPResponseError{
		Error:   false,
		Message: "consumer group removed",
		Request: requestInfo,
	})
}

func handleBrokerTopicList(app *ApplicationContext, w http.ResponseWriter, r *http.Request, cluster string) int {
	storageRequest := &RequestTopicList{Result: make(chan *ResponseTopicList), Cluster: cluster}
	app.Storage.requestChannel <- storageRequest
	result := <-storageRequest.Result

	requestInfo := makeRequestInfo(r)
	requestInfo.Cluster = cluster
	return writeJSONResponse(w, 200, HTTPResponseTopicList{
		Error:   false,
		Message: "broker topic list returned",
		Topics:  result.TopicList,
		Request: requestInfo,
	})
}

func handleBrokerTopicDetail(app *ApplicationContext, w http.ResponseWriter, r *http.Request, cluster string, topic string) int {
	storageRequest := &RequestOffsets{Result: make(chan *ResponseOffsets), Cluster: cluster, Topic: topic}
	app.Storage.requestChannel <- storageRequest
	result := <-storageRequest.Result
	if result.ErrorTopic {
		return makeErrorResponse(http.StatusNotFound, "topic not found", w, r)
	}

	requestInfo := makeRequestInfo(r)
	requestInfo.Cluster = cluster
	requestInfo.Topic = topic
	return writeJSONResponse(w, 200, HTTPResponseTopicDetail{
		Error:   false,
		Message: "broker topic offsets returned",
		Offsets: result.OffsetList,
		Request: requestInfo,
	})
}

func (server *HttpServer) Stop() {
	// Nothing to do right now
}
