package server

import (
	"net/http"
	"strings"

	"github.com/vrypan/listnr/internal/ap"
	"github.com/vrypan/listnr/internal/buildinfo"
	"github.com/vrypan/listnr/internal/httpcache"
)

const (
	// nodeInfoSchema21 is the rel used to advertise a NodeInfo 2.1 document.
	nodeInfoSchema21 = "http://nodeinfo.diaspora.software/ns/schema/2.1"
	// nodeInfoContentType is the media type the NodeInfo protocol specifies
	// for a schema document, carrying the schema as a profile parameter.
	nodeInfoContentType  = `application/json; profile="http://nodeinfo.diaspora.software/ns/schema/2.1#"; charset=utf-8`
	discoveryContentType = "application/json; charset=utf-8"
	// repositoryURL is the canonical home of the software, published as both
	// repository and homepage.
	repositoryURL = "https://github.com/vrypan/listnr"
)

// nodeInfoLinks is the JRD-style discovery document at /.well-known/nodeinfo.
type nodeInfoLinks struct {
	Links []nodeInfoLink `json:"links"`
}

type nodeInfoLink struct {
	Rel  string `json:"rel"`
	Href string `json:"href"`
}

// nodeInfo is a NodeInfo 2.1 document. The slice and map fields are always
// allocated so they serialize as [] and {} rather than null, which the schema
// requires.
type nodeInfo struct {
	Version           string           `json:"version"`
	Software          nodeInfoSoftware `json:"software"`
	Protocols         []string         `json:"protocols"`
	Services          nodeInfoServices `json:"services"`
	OpenRegistrations bool             `json:"openRegistrations"`
	Usage             nodeInfoUsage    `json:"usage"`
	Metadata          map[string]any   `json:"metadata"`
}

type nodeInfoSoftware struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Repository string `json:"repository"`
	Homepage   string `json:"homepage"`
}

type nodeInfoServices struct {
	Inbound  []string `json:"inbound"`
	Outbound []string `json:"outbound"`
}

type nodeInfoUsage struct {
	Users         nodeInfoUsers `json:"users"`
	LocalPosts    int           `json:"localPosts"`
	LocalComments int           `json:"localComments"`
}

type nodeInfoUsers struct {
	Total          int `json:"total"`
	ActiveMonth    int `json:"activeMonth"`
	ActiveHalfyear int `json:"activeHalfyear"`
}

// handleNodeInfoDiscovery advertises where the NodeInfo document lives. It
// deliberately touches no database state, so discovery keeps working even if
// the statistics query fails.
func (s *Server) handleNodeInfoDiscovery(w http.ResponseWriter, r *http.Request) {
	doc := nodeInfoLinks{Links: []nodeInfoLink{{
		Rel:  nodeInfoSchema21,
		Href: "https://" + s.cfg.Actor.Host + "/nodeinfo/2.1",
	}}}
	if err := httpcache.WriteJSON(w, r, discoveryContentType, ap.CacheControl, doc); err != nil {
		s.log.Error("write nodeinfo discovery failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
	}
}

func (s *Server) handleNodeInfo(w http.ResponseWriter, r *http.Request) {
	// Only active federated posts count: unfederated and withdrawn rows are
	// not part of what this node publishes.
	posts, err := s.st.PostCount()
	if err != nil {
		s.log.Error("nodeinfo post count failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	doc := nodeInfo{
		Version: "2.1",
		Software: nodeInfoSoftware{
			Name:       "listnr",
			Version:    softwareVersion(),
			Repository: repositoryURL,
			Homepage:   repositoryURL,
		},
		Protocols:         []string{"activitypub"},
		Services:          nodeInfoServices{Inbound: []string{}, Outbound: []string{}},
		OpenRegistrations: false,
		Usage: nodeInfoUsage{
			// listnr serves exactly one actor, and it is active by virtue of
			// the daemon running. Real activity windows would mean tracking
			// data listnr has no other use for.
			Users:      nodeInfoUsers{Total: 1, ActiveMonth: 1, ActiveHalfyear: 1},
			LocalPosts: posts,
			// Replies come from remote actors on their own instances, so they
			// are not local comments.
			LocalComments: 0,
		},
		Metadata: map[string]any{},
	}
	if err := httpcache.WriteJSON(w, r, nodeInfoContentType, ap.CacheControl, doc); err != nil {
		s.log.Error("write nodeinfo failed", "err", err)
		http.Error(w, "server error", http.StatusInternalServerError)
	}
}

// softwareVersion reports the running version without a leading "v",
// preserving any development or build suffix.
func softwareVersion() string {
	return strings.TrimPrefix(buildinfo.Current().Version, "v")
}
