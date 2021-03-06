package libapiserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/ipcjk/ixgen/inireader"
	"github.com/ipcjk/ixgen/ixtypes"
	"github.com/ipcjk/ixgen/ixworkers"
	"github.com/ipcjk/ixgen/peergen"
	"github.com/ipcjk/ixgen/peeringdb"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var matchIxRegex = `\/api\/ix\/(\d+)$`
var matchIxLanRegex = `\/api\/ixlan\/(\d+)$`
var matchStyleRegex = `\/ixgen\/(\w+)\/(\w+)\/?(\d+)?$`

type handler struct {
	CacheDir string
	Data     []byte
	mutex    sync.Mutex
}

type netHandler struct {
	handler
	NetData peeringdb.Net
}

type Apiserver struct {
	AddrPort    string
	CacheDir    string
	templateDir string
}

type getIX struct {
	handler
	match *regexp.Regexp
}

type getIXLan struct {
	handler
	match *regexp.Regexp
}

type postConfig struct {
	match     *regexp.Regexp
	addrPort  string
	templates string
}

type getIXLans handler
type getIXes handler
type getNetIXLan handler
type getFac handler
type getNet netHandler
type getAll handler

// NewAPIServer returns a new Apiserver object, than can be
// started to answer to peeringdb-style api questions.
//
// It will take the ListenAddr and Port and also a source directory where
// to serve the object files from as arguments.
//
// It also can take a POST request with an INI- or JSON-style configuration
//
func NewAPIServer(addrport, cacheDir string, templatedir string) *Apiserver {
	return &Apiserver{addrport, cacheDir, templatedir}
}

// RunAPIServer starts the created Apiserver
func (a *Apiserver) RunAPIServer() {
	r := http.NewServeMux()
	matchIx, _ := regexp.Compile(matchIxRegex)
	matchIxLan, _ := regexp.Compile(matchIxLanRegex)
	matchStyle, _ := regexp.Compile(matchStyleRegex)

	listener, err := net.Listen("tcp", a.AddrPort)
	if err != nil {
		log.Fatalf("Cant spin up local api-service: %s", err)
	}
	a.AddrPort = listener.Addr().String()

	/* PeeringDBI API clone */
	r.Handle("/api/ix", &getIXes{a.CacheDir, nil, sync.Mutex{}})
	r.Handle("/api/ix/", &getIX{handler{a.CacheDir, nil, sync.Mutex{}}, matchIx})
	r.Handle("/api/netixlan", &getNetIXLan{a.CacheDir, nil, sync.Mutex{}})
	r.Handle("/api/net", &getNet{handler{a.CacheDir, nil, sync.Mutex{}}, peeringdb.Net{}})
	r.Handle("/api/ixlan", &getIXLans{a.CacheDir, nil, sync.Mutex{}})
	r.Handle("/api/ixlan/", &getIXLan{handler{a.CacheDir, nil, sync.Mutex{}}, matchIxLan})

	/* Post/Get Configuration */
	r.Handle("/ixgen/", &postConfig{match: matchStyle, addrPort: a.AddrPort, templates: a.templateDir})

	go http.Serve(listener, r)
}

func getJSON(r io.Reader, i interface{}) {
	err := json.NewDecoder(r).Decode(&i)
	if err != nil {
		log.Fatal("Problems decoding from json")
	}
}

func writeJSON(w io.Writer, i interface{}) {
	err := json.NewEncoder(w).Encode(&i)
	if err != nil {
		log.Fatal("Problems encoding ix from json")
	}
}

func readFile(fileName string) []byte {
	file, err := os.Open(fileName)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	buf := new(bytes.Buffer)

	_, err = buf.ReadFrom(file)
	if err != nil {
		log.Fatalf("Cant read from file :%s", fileName)
	}
	return buf.Bytes()
}

func (h *postConfig) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var exchanges ixtypes.IXs
	var myASN int64
	var err error

	defer r.Body.Close()
	ct := r.Header.Get("Content-Type")

	if r.Method != "POST" {
		fmt.Fprint(w, "Not enough arguments given, IX not found or JSON malformed")
		return
	}

	matches := h.match.FindStringSubmatch(r.RequestURI)
	if len(matches) < 3 {
		fmt.Fprint(w, "Not enough arguments given, IX not found or JSON malformed")
		return
	}

	if matches[3] != "" {
		myASN, err = strconv.ParseInt(matches[3], 10, 64)
		if err != nil {
			myASN = 0
		}
	}

	peerStyle := fmt.Sprintf("%s/%s", matches[1], matches[2])
	peerGenerator := peergen.NewPeerGen(peerStyle, h.templates)

	/* JSON or plain incoming? */
	if ct == "application/json" {
		err := json.NewDecoder(r.Body).Decode(&exchanges)
		if err != nil {
			fmt.Fprintf(w, "JSON malformed: %s", err)
			return
		}
	} else {
		exchanges = inireader.ReadPeeringConfig(r.Body)
	}

	if len(exchanges) == 0 {
		fmt.Fprint(w, "Not enough arguments given, IX not found or JSON malformed")
		return
	}

	exchanges = ixworkers.WorkerMergePeerConfiguration(exchanges, "http://"+h.addrPort+"/api", "", myASN)
	if strings.Contains(matches[2], "json") {
		w.Header().Set("content-type:", "application/json")
	} else {
		w.Header().Set("content-type:", "text/plain")
	}
	peerGenerator.GenerateIXs(exchanges, w)
}

func (h *getNet) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var apiResult peeringdb.Net
	params := r.URL.Query()

	h.mutex.Lock()
	/* Only the first request, will load the file into our structure */
	if len(h.Data) == 0 {
		h.Data = readFile(h.CacheDir + "/net")
		getJSON(bytes.NewBuffer(h.Data), &h.NetData)
	}
	h.mutex.Unlock()

	/* No params? Then write out all */
	if len(params) == 0 {
		apiResult = h.NetData
		goto end
	}

	/* Search for the network, can be optimized by loading things into a HASH */
	for k := range h.NetData.Data {
		if params["asn"][0] == strconv.FormatInt(h.NetData.Data[k].Asn, 10) {
			apiResult.Data = append(apiResult.Data, h.NetData.Data[k])
			break
		}
	}
end:
	writeJSON(w, &apiResult)
}

func (h *getNetIXLan) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var data peeringdb.Netixlan
	var apiResult peeringdb.Netixlan

	params := r.URL.Query()

	h.mutex.Lock()
	if len(h.Data) == 0 {
		h.Data = readFile(h.CacheDir + "/netixlan")
	}
	h.mutex.Unlock()

	getJSON(bytes.NewBuffer(h.Data), &data)

	if len(params) == 0 {
		apiResult = data
		goto end
	}

	for k := range data.Data {
		if params["ixlan_id"][0] == strconv.FormatInt(data.Data[k].IxlanID, 10) {
			apiResult.Data = append(apiResult.Data, data.Data[k])
		}
	}
end:
	writeJSON(w, &apiResult)
}

func (h *getIXLan) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var data peeringdb.IxLAN
	var apiResult peeringdb.IxLAN

	matches := h.match.FindStringSubmatch(r.RequestURI)
	h.mutex.Lock()
	if len(h.Data) == 0 {
		h.Data = readFile(h.CacheDir + "/ixlan")
	}
	h.mutex.Unlock()

	getJSON(bytes.NewBuffer(h.Data), &data)

	for k := range data.Data {
		if strconv.FormatInt(data.Data[k].ID, 10) == matches[1] {
			apiResult.Data = append(apiResult.Data, data.Data[k])
			break
		}
	}

	writeJSON(w, &apiResult)
}

func (h *getIXLans) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var data peeringdb.IxLAN
	var apiResult peeringdb.IxLAN

	params := r.URL.Query()

	h.mutex.Lock()
	if len(h.Data) == 0 {
		h.Data = readFile(h.CacheDir + "/ixlan")
	}
	h.mutex.Unlock()

	getJSON(bytes.NewBuffer(h.Data), &data)

	if len(params) == 0 {
		apiResult = data
		goto end
	}

	for k := range data.Data {
		for kp, kv := range params {
			if kp == "ix_id" && strconv.FormatInt(data.Data[k].IxID, 10) == kv[0] {
				apiResult.Data = append(apiResult.Data, data.Data[k])
			}
		}
	}

end:
	writeJSON(w, &apiResult)
}

func (h *getIX) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var data peeringdb.Ix
	var apiResult peeringdb.Ix

	matches := h.match.FindStringSubmatch(r.RequestURI)
	h.mutex.Lock()
	if len(h.Data) == 0 {
		h.Data = readFile(h.CacheDir + "/ix")
	}
	h.mutex.Unlock()

	getJSON(bytes.NewBuffer(h.Data), &data)

	for k := range data.Data {
		if strconv.FormatInt(data.Data[k].ID, 10) == matches[1] {
			apiResult.Data = append(apiResult.Data, data.Data[k])
			break
		}
	}

	/* populate ixlan_set */
	var ixLanData peeringdb.IxLAN
	ixData := readFile(h.CacheDir + "/ixlan")
	getJSON(bytes.NewBuffer(ixData), &ixLanData)

	for k := range ixLanData.Data {
		if strconv.FormatInt(ixLanData.Data[k].IxID, 10) == matches[1] {
			apiResult.Data[0].IxlanSet =
				append(apiResult.Data[0].IxlanSet, ixLanData.Data[k])
		}
	}

	writeJSON(w, &apiResult)
}

func (h *getIXes) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var data peeringdb.Ix
	var apiResult peeringdb.Ix

	params := r.URL.Query()

	h.mutex.Lock()
	if len(h.Data) == 0 {
		h.Data = readFile(h.CacheDir + "/ix")
	}
	h.mutex.Unlock()

	getJSON(bytes.NewBuffer(h.Data), &data)

	if len(params) == 0 {
		apiResult = data
		goto end
	}

	for k := range data.Data {
		var hits = 0
		for kp, kv := range params {
			if kp == "name" && data.Data[k].Name == kv[0] {
				hits++
			} else if kp == "id" && strconv.FormatInt(data.Data[k].ID, 10) == kv[0] {
				hits++
			}
		}
		if hits == len(params) {
			apiResult.Data = append(apiResult.Data, data.Data[k])
			break
		}
	}

end:
	writeJSON(w, &apiResult)
}

func (h *getFac) ServeHTTP(w http.ResponseWriter, r *http.Request) {
}
