package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"text/template"

	"github.com/pkg/errors"
	"go.uber.org/zap"

	v1 "github.com/kubeshop/testkube/internal/app/api/metrics"
	"github.com/kubeshop/testkube/pkg/api/v1/testkube"
	"github.com/kubeshop/testkube/pkg/event/kind/common"
	thttp "github.com/kubeshop/testkube/pkg/http"
	"github.com/kubeshop/testkube/pkg/log"
	"github.com/kubeshop/testkube/pkg/repository/result"
	"github.com/kubeshop/testkube/pkg/repository/testresult"
	"github.com/kubeshop/testkube/pkg/repository/testworkflow"
	"github.com/kubeshop/testkube/pkg/utils"
	"github.com/kubeshop/testkube/pkg/utils/text"
)

var _ common.Listener = (*WebhookListener)(nil)

func NewWebhookListener(name, uri, selector string, events []testkube.EventType,
	payloadObjectField, payloadTemplate string, headers map[string]string, disabled bool,
	onStateChange bool,
	testExecutionResults result.Repository,
	testSuiteExecutionResults testresult.Repository,
	testWorkflowExecutionResults testworkflow.Repository, metrics v1.Metrics) *WebhookListener {
	return &WebhookListener{
		name:                         name,
		Uri:                          uri,
		Log:                          log.DefaultLogger,
		HttpClient:                   thttp.NewClient(),
		selector:                     selector,
		events:                       events,
		payloadObjectField:           payloadObjectField,
		payloadTemplate:              payloadTemplate,
		headers:                      headers,
		disabled:                     disabled,
		onStateChange:                onStateChange,
		testExecutionResults:         testExecutionResults,
		testSuiteExecutionResults:    testSuiteExecutionResults,
		testWorkflowExecutionResults: testWorkflowExecutionResults,
		metrics:                      metrics,
	}
}

type WebhookListener struct {
	name                         string
	Uri                          string
	Log                          *zap.SugaredLogger
	HttpClient                   *http.Client
	events                       []testkube.EventType
	selector                     string
	payloadObjectField           string
	payloadTemplate              string
	headers                      map[string]string
	disabled                     bool
	onStateChange                bool
	testExecutionResults         result.Repository
	testSuiteExecutionResults    testresult.Repository
	testWorkflowExecutionResults testworkflow.Repository
	metrics                      v1.Metrics
}

func (l *WebhookListener) Name() string {
	return common.ListenerName(l.name)
}

func (l *WebhookListener) Selector() string {
	return l.selector
}

func (l *WebhookListener) Events() []testkube.EventType {
	return l.events
}
func (l *WebhookListener) Metadata() map[string]string {
	return map[string]string{
		"name":               l.Name(),
		"uri":                l.Uri,
		"selector":           l.selector,
		"events":             fmt.Sprintf("%v", l.events),
		"payloadObjectField": l.payloadObjectField,
		"payloadTemplate":    l.payloadTemplate,
		"headers":            fmt.Sprintf("%v", l.headers),
		"disabled":           fmt.Sprint(l.disabled),
		"onStateChange":      fmt.Sprint(l.onStateChange),
	}
}

func (l *WebhookListener) PayloadObjectField() string {
	return l.payloadObjectField
}

func (l *WebhookListener) PayloadTemplate() string {
	return l.payloadTemplate
}

func (l *WebhookListener) Headers() map[string]string {
	return l.headers
}

func (l *WebhookListener) Disabled() bool {
	return l.disabled
}

func (l *WebhookListener) Notify(event testkube.Event) (result testkube.EventResult) {
	defer func() {
		var eventType, res string
		if event.Type_ != nil {
			eventType = string(*event.Type_)
		}

		res = "success"
		if result.Error() != "" {
			res = "error"
		}

		l.metrics.InWebhookEventCount(l.name, eventType, res)
	}()

	switch {
	case l.disabled:
		l.Log.With(event.Log()...).Debug("webhook listener is disabled")
		result = testkube.NewSuccessEventResult(event.Id, "webhook listener is disabled")
		return
	case event.TestExecution != nil && event.TestExecution.DisableWebhooks:
		l.Log.With(event.Log()...).Debug("webhook listener is disabled for test execution")
		result = testkube.NewSuccessEventResult(event.Id, "webhook listener is disabled for test execution")
		return
	case event.TestSuiteExecution != nil && event.TestSuiteExecution.DisableWebhooks:
		l.Log.With(event.Log()...).Debug("webhook listener is disabled for test suite execution")
		result = testkube.NewSuccessEventResult(event.Id, "webhook listener is disabled for test suite execution")
		return
	case event.TestWorkflowExecution != nil && event.TestWorkflowExecution.DisableWebhooks:
		l.Log.With(event.Log()...).Debug("webhook listener is disabled for test workflow execution")
		result = testkube.NewSuccessEventResult(event.Id, "webhook listener is disabled for test workflow execution")
		return
	}

	if l.onStateChange {
		changed, err := l.hasStateChanges(event)
		if err != nil {
			l.Log.With(event.Log()...).Errorw(fmt.Sprintf("could not get previous finished state for test %s", event.TestExecution.TestName), "error", err)
		}
		if !changed {
			return testkube.NewSuccessEventResult(event.Id, "webhook set to state change only; state has not changed")
		}
	}

	body := bytes.NewBuffer([]byte{})
	log := l.Log.With(event.Log()...)

	var err error
	if l.payloadTemplate != "" {
		var data []byte
		data, err = l.processTemplate("payload", l.payloadTemplate, event)
		if err != nil {
			result = testkube.NewFailedEventResult(event.Id, err)
			return
		}

		_, err = body.Write(data)
	} else {
		err = json.NewEncoder(body).Encode(event)
		if err == nil && l.payloadObjectField != "" {
			data := map[string]string{l.payloadObjectField: string(body.Bytes())}
			body.Reset()
			err = json.NewEncoder(body).Encode(data)
		}
	}

	if err != nil {
		err = errors.Wrap(err, "webhook send encode error")
		log.Errorw("webhook send encode error", "error", err)
		result = testkube.NewFailedEventResult(event.Id, err)
		return
	}

	data, err := l.processTemplate("uri", l.Uri, event)
	if err != nil {
		result = testkube.NewFailedEventResult(event.Id, err)
		return
	}

	request, err := http.NewRequest(http.MethodPost, string(data), body)
	if err != nil {
		log.Errorw("webhook request creating error", "error", err)
		result = testkube.NewFailedEventResult(event.Id, err)
		return
	}

	request.Header.Set("Content-Type", "application/json")
	for key, value := range l.headers {
		values := []*string{&key, &value}
		for i := range values {
			data, err = l.processTemplate("header", *values[i], event)
			if err != nil {
				result = testkube.NewFailedEventResult(event.Id, err)
				return
			}

			*values[i] = string(data)
		}

		request.Header.Set(key, value)
	}

	resp, err := l.HttpClient.Do(request)
	if err != nil {
		log.Errorw("webhook send error", "error", err)
		result = testkube.NewFailedEventResult(event.Id, err)
		return
	}
	defer resp.Body.Close()

	data, err = io.ReadAll(resp.Body)
	if err != nil {
		log.Errorw("webhook read response error", "error", err)
		result = testkube.NewFailedEventResult(event.Id, err)
		return
	}

	responseStr := string(data)

	if resp.StatusCode >= 400 {
		err := fmt.Errorf("webhook response with bad status code: %d", resp.StatusCode)
		log.Errorw("webhook send error", "error", err, "status", resp.StatusCode)
		result = testkube.NewFailedEventResult(event.Id, err).WithResult(responseStr)
		return
	}

	log.Debugw("got webhook send result", "response", responseStr)
	result = testkube.NewSuccessEventResult(event.Id, responseStr)
	return
}

func (l *WebhookListener) Kind() string {
	return "webhook"
}

func (l *WebhookListener) processTemplate(field, body string, event testkube.Event) ([]byte, error) {
	log := l.Log.With(event.Log()...)

	var tmpl *template.Template
	tmpl, err := utils.NewTemplate(field).Funcs(template.FuncMap{
		"tostr":                            text.ToStr,
		"executionstatustostring":          testkube.ExecutionStatusString,
		"testsuiteexecutionstatustostring": testkube.TestSuiteExecutionStatusString,
		"testworkflowstatustostring":       testkube.TestWorkflowStatusString,
	}).Parse(body)
	if err != nil {
		log.Errorw(fmt.Sprintf("creating webhook %s error", field), "error", err)
		return nil, err
	}

	var buffer bytes.Buffer
	if err = tmpl.ExecuteTemplate(&buffer, field, event); err != nil {
		log.Errorw(fmt.Sprintf("executing webhook %s error", field), "error", err)
		return nil, err
	}

	return buffer.Bytes(), nil
}

func (l *WebhookListener) hasStateChanges(event testkube.Event) (bool, error) {
	log := l.Log.With(event.Log()...)

	if event.TestExecution != nil && event.TestExecution.ExecutionResult != nil {
		prevStatus, err := l.testExecutionResults.GetPreviousFinishedState(context.Background(), event.TestExecution.TestName, event.TestExecution.EndTime)
		if err != nil {
			return false, err
		}
		if prevStatus == "" {
			log.Debugw(fmt.Sprintf("no previous finished state for test %s", event.TestExecution.TestName))
			return true, nil
		}

		return *event.TestExecution.ExecutionResult.Status != prevStatus, nil
	}

	if event.TestSuiteExecution != nil && event.TestSuiteExecution.Status != nil {
		prevStatus, err := l.testSuiteExecutionResults.GetPreviousFinishedState(context.Background(), event.TestSuiteExecution.TestSuite.Name, event.TestSuiteExecution.EndTime)
		if err != nil {
			log.Errorw(fmt.Sprintf("could not get previous finished state for test suite %s", event.TestSuiteExecution.TestSuite.Name), "error", err)
			return false, err
		}
		if prevStatus == "" {
			log.Debugw(fmt.Sprintf("no previous finished state for test suite %s", event.TestSuiteExecution.TestSuite.Name))
			return true, nil
		}

		return *event.TestSuiteExecution.Status != prevStatus, nil
	}

	if event.TestWorkflowExecution != nil && event.TestWorkflowExecution.Result != nil {
		prevStatus, err := l.testWorkflowExecutionResults.GetPreviousFinishedState(context.Background(), event.TestWorkflowExecution.Workflow.Name, event.TestWorkflowExecution.StatusAt)

		if err != nil {
			log.Errorw(fmt.Sprintf("could not get previous finished state for test workflow %s", event.TestWorkflowExecution.Workflow.Name), "error", err)
			return false, err
		}
		if prevStatus == "" {
			log.Debugw(fmt.Sprintf("no previous finished state for test workflow %s", event.TestWorkflowExecution.Workflow.Name))
			return true, nil
		}
		return *event.TestWorkflowExecution.Result.Status != prevStatus, nil
	}

	return false, nil
}
