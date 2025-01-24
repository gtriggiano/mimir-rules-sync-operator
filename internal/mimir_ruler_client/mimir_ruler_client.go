package mimirrulerclient

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/gtriggiano/mimir-rules-sync/api/v1alpha1"
	"gopkg.in/yaml.v3"
)

// MimirRulerClient is a client for the Mimir Ruler API
type MimirRulerClient struct {
	mimirRulerUrl *url.URL
	http          *http.Client
	auth          *mimirBasicAuth
}

// MimirRuleGroupsByNamespace is a map of namespace to rule groups
type MimirRuleGroupsByNamespace map[string][]MimirRuleGroup

// MimirRuleGroup is a list of sequentially evaluated recording and alerting rules.
type MimirRuleGroup struct {
	Name     string             `json:"name"`
	Interval *v1alpha1.Duration `json:"interval"`
	Rules    []MimirRule        `json:"rules"`
}

// MimirRule describes an alerting or recording rule
type MimirRule struct {
	Record      string             `json:"record,omitempty"`
	Alert       string             `json:"alert,omitempty"`
	Expr        string             `json:"expr"`
	For         *v1alpha1.Duration `json:"for,omitempty"`
	Labels      map[string]string  `json:"labels,omitempty"`
	Annotations map[string]string  `json:"annotations,omitempty"`
}

type mimirBasicAuth struct {
	username string
	password string
}

func NewMimirRulerClient(mimirRulerUrl, mimirUser, mimirPassword string, tlsInsecureSkipVerify bool) (*MimirRulerClient, error) {
	mimirRulerUrlParsed, err := url.Parse(mimirRulerUrl)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).Dial,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: tlsInsecureSkipVerify},
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		DisableKeepAlives:     false,
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	var auth *mimirBasicAuth
	if mimirUser != "" && mimirPassword != "" {
		auth = &mimirBasicAuth{
			username: mimirUser,
			password: mimirPassword,
		}
	}

	return &MimirRulerClient{
		mimirRulerUrl: mimirRulerUrlParsed,
		http:          client,
		auth:          auth,
	}, nil
}

// makeRequest makes a request to the Mimir Ruler API
func (client *MimirRulerClient) makeRequest(request *http.Request) (int, string, error) {
	resp, err := client.http.Do(request)
	if err != nil {
		return 0, "error", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "error", err
	}
	return resp.StatusCode, string(body), err
}

// buildRequest builds a request to the Mimir Ruler API
func (client *MimirRulerClient) buildRequest(method, path, tenant, accept, contentType string, payload io.Reader) (*http.Request, error) {
	urlPath := client.mimirRulerUrl.Path + path
	requestUrl := client.mimirRulerUrl.ResolveReference(&url.URL{Path: urlPath}).String()

	req, err := http.NewRequest(method, requestUrl, payload)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mimir-rules-sync")
	req.Header.Set("Accept", accept)

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if tenant != "" {
		req.Header.Set("X-Scope-OrgID", tenant)
	}

	if client.auth != nil {
		req.SetBasicAuth(client.auth.username, client.auth.password)
	}

	return req, nil
}

// SetRuleGroup sets a rule group in the Mimir Ruler API
func (client *MimirRulerClient) SetRuleGroup(tenant, namespace string, ruleGroup *MimirRuleGroup) (string, error) {
	requestURL := fmt.Sprintf("/config/v1/rules/%s", namespace)
	requestBody, _ := yaml.Marshal(ruleGroup)
	request, err := client.buildRequest(http.MethodPost, requestURL, tenant, "application/json", "application/yaml", bytes.NewReader(requestBody))
	if err != nil {
		return "error", err
	}
	statusCode, out, err := client.makeRequest(request)
	if statusCode >= 200 && statusCode <= 299 {
		return out, err
	}
	return out, fmt.Errorf("%d POST %s", statusCode, request.URL)
}

// GetNamespaceRuleGroups gets all rule groups in a namespace from the Mimir Ruler API
func (ruler *MimirRulerClient) GetNamespaceRuleGroups(tenant, namespace string) ([]MimirRuleGroup, error) {
	requestURL := fmt.Sprintf("/config/v1/rules/%s", namespace)
	request, err := ruler.buildRequest(http.MethodGet, requestURL, tenant, "application/yaml", "", nil)
	if err != nil {
		return []MimirRuleGroup{}, err
	}
	statusCode, out, err := ruler.makeRequest(request)
	if !(statusCode >= 200 && statusCode <= 299) || (err != nil) {
		return []MimirRuleGroup{}, fmt.Errorf("%d GET %s\n%s", statusCode, request.URL, out)
	}
	namespaceRules := MimirRuleGroupsByNamespace{}
	err = yaml.Unmarshal([]byte(out), &namespaceRules)
	if err != nil {
		return []MimirRuleGroup{}, err
	}
	// The API returns always empty yaml even if the namespace is not found
	if rules, found := namespaceRules[namespace]; found {
		return rules, err
	}
	return []MimirRuleGroup{}, nil
}

// DeleteRuleGroup deletes a rule group from the Mimir Ruler API
func (ruler *MimirRulerClient) DeleteRuleGroup(tenant, namespace, groupName string) (string, error) {
	requestURL := fmt.Sprintf("/config/v1/rules/%s/%s", namespace, groupName)
	request, err := ruler.buildRequest(http.MethodDelete, requestURL, tenant, "application/yaml", "", nil)
	if err != nil {
		return "error", err
	}
	statusCode, out, err := ruler.makeRequest(request)
	if statusCode >= 200 && statusCode <= 299 {
		return out, err
	}
	return out, fmt.Errorf("%d DELETE %s", statusCode, request.URL)
}

// DeleteNamespace deletes a namespace from the Mimir Ruler API
func (ruler *MimirRulerClient) DeleteNamespace(tenant, namespace string) (string, error) {
	requestURL := fmt.Sprintf("/config/v1/rules/%s", namespace)
	request, err := ruler.buildRequest(http.MethodDelete, requestURL, tenant, "application/yaml", "", nil)
	if err != nil {
		return "error", err
	}
	statusCode, out, err := ruler.makeRequest(request)
	if statusCode >= 200 && statusCode <= 299 {
		return out, err
	}
	return out, fmt.Errorf("%d DELETE %s", statusCode, request.URL)
}
