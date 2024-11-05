package util

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/oliveagle/jsonpath"
	"github.com/ramendr/ramen/internal/controller/kubeobjects"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type expressionResult struct {
	result bool
	err    error
}

func EvaluateJsonPathExpression(cli client.Client, captureGroup *kubeobjects.CaptureSpec, log logr.Logger) (bool, error) {
	timeout := getTimeoutValue(&captureGroup.Hook)
	nsScopedName := types.NamespacedName{
		Namespace: captureGroup.Hook.Namespace,
		Name:      captureGroup.Hook.Name,
	}

	var resource client.Object

	switch captureGroup.Hook.SelectResource {
	case "pod":
		// handle pod type
		resource = &corev1.Pod{}
	case "deployment":
		// handle deployment type
		log.Info("**** ASN, in case deployment for check hooks")

		resource = &appsv1.Deployment{}
	case "statefulset":
		// handle statefulset type
		resource = &appsv1.StatefulSet{}
	}

	err := WaitUntilResourceExists(cli, resource, nsScopedName, time.Duration(timeout))
	if err != nil {
		return false, err
	}

	data, err := json.MarshalIndent(resource, "", "  ")
	if err != nil {
		return false, err
	}

	return evaluateJPExp(captureGroup, data)
}

func getTimeoutValue(hook *kubeobjects.HookSpec) int {
	if hook.Chk.Timeout != 0 {
		return hook.Chk.Timeout
	} else if hook.Timeout != 0 {
		return hook.Timeout
	} else {
		// 300s is the default value for timeout
		return 300
	}
}

func evaluateJPExp(captureGroup *kubeobjects.CaptureSpec, data []byte) (bool, error) {
	var ifData interface{}

	err := json.Unmarshal(data, &ifData)
	if err != nil {
		return false, err
	}

	jsonCondition := captureGroup.Hook.Chk.Condition

	op, paths, err := getJSONPathsAndOp(jsonCondition)
	if err != nil {
		return false, err
	}

	tPaths := trimPaths(paths)
	results := make([]interface{}, len(paths))

	for i, path := range tPaths {
		// This section is using go get github.com/oliveagle/jsonpath -- This seems to work. Enhance based on this, we will see later
		if !strings.HasPrefix(path, "$.") {
			// It might not be a json path and might be a base type
			results[i] = path

			continue
		}

		results[i], err = jsonpath.JsonPathLookup(ifData, path)
		if err != nil {
			return false, err
		}
	}

	fmt.Println("**** ASN, the results are ", results)
	expValue, err := evaluateResults(op, results[0], results[1])

	fmt.Println("**** ASN, expression evaluated value with results ", expValue)

	if err != nil {
		return false, err
	}

	return expValue, nil
}

func evaluateResults(op string, valA, valB interface{}) (bool, error) {
	switch op {
	case "==":
		return valA == valB, nil
	case "!=":
		return valA != valB, nil
	case "<":
		return valA.(string) < valB.(string), nil
	case ">":
		return valA.(string) > valB.(string), nil
	case "<=":
		return valA.(string) <= valB.(string), nil
	case ">=":
		return valA.(string) >= valB.(string), nil
	default:
		return false, fmt.Errorf("unsupported op %s provided for jsonpath evalvation", op)
	}
}

func trimPaths(paths []string) []string {
	tPaths := make([]string, len(paths))

	for i, path := range paths {
		path = strings.TrimSpace(path)
		path = strings.TrimLeft(path, "{")
		path = strings.TrimRight(path, "}")
		tPaths[i] = path
	}

	return tPaths
}

func getJSONPathsAndOp(exp string) (string, []string, error) {
	operators := []string{"==", "!=", ">=", ">", "<=", "<"}
	for _, op := range operators {
		if strings.Contains(exp, op) {
			return op, strings.Split(exp, op), nil
		}
	}

	return "", []string{}, errors.New("unsupported operator used in jsonpath evaluation")
}

func WaitUntilResourceExists(client client.Client, obj client.Object, nsScopedName types.NamespacedName,
	timeout time.Duration,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond) // Poll every 100 milliseconds
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for resource %s to be ready: %w", nsScopedName.Name, ctx.Err())
		case <-ticker.C:
			err := client.Get(context.Background(), nsScopedName, obj)
			if err != nil {
				if k8serrors.IsNotFound(err) {
					// Resource not found, continue polling
					continue
				}

				return err // Some other error occurred, return it
			}

			return nil // Resource is ready
		}
	}
}
