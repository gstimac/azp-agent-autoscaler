package kubernetes

import (
	"fmt"
	"os"
	"strings"

	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	apimachinery "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8s "k8s.io/client-go/kubernetes"
	k8srest "k8s.io/client-go/rest"
	k8sclientcmd "k8s.io/client-go/tools/clientcmd"
)

type k8sClientSet struct {
	client *k8s.Clientset
}

var k8sClient = k8sClientSet{}

// GetK8sClient returns a Kubernetes client, which is cached
func (wrapper *k8sClientSet) getClient() (*k8s.Clientset, error) {
	if wrapper.client != nil {
		return wrapper.client, nil
	}

	k8sConfig, err := k8srest.InClusterConfig()
	if err != nil {
		kubeconfigEnv := os.Getenv("KUBECONFIG")
		k8sConfig, err = k8sclientcmd.BuildConfigFromFlags("", kubeconfigEnv)
		if err != nil {
			home := os.Getenv("HOME")
			if home == "" {
				home = os.Getenv("USERPROFILE") // windows
			}
			k8sConfig, err = k8sclientcmd.BuildConfigFromFlags("", fmt.Sprintf("%s/.kube/config", home))
			if err != nil {
				return nil, fmt.Errorf("Error initializing Kubernetes config: %s", err.Error())
			}
		}
	}

	clientset, err := k8s.NewForConfig(k8sConfig)
	if err == nil {
		wrapper.client = clientset
	}
	return clientset, err
}

// GetK8sWorkload retrieves a Workload
func GetK8sWorkload(channel chan<- WorkloadReturn, kind string, namespace string, name string) {
	if strings.EqualFold(kind, "StatefulSet") {
		channel <- getStatefulSet(namespace, name)
	} else {
		channel <- GetWorkloadReturn(nil, fmt.Errorf("Resource kind %s is not implemented", kind))
	}
}

func getStatefulSet(namespace string, name string) WorkloadReturn {
	client, err := k8sClient.getClient()
	if err != nil {
		return GetWorkloadReturn(nil, err)
	}
	statefulSet, err := client.AppsV1().StatefulSets(namespace).Get(name, metav1.GetOptions{})
	if err != nil {
		return GetWorkloadReturn(nil, err)
	} else if statefulSet == nil {
		return GetWorkloadReturn(nil, fmt.Errorf("Could not find statefulset/%s in namespace %s", name, namespace))
	} else {
		return GetWorkload(statefulSet)
	}
}

// VerifyNoHorizontalPodAutoscaler returns an error if the given resource has a HorizontalPodAutoscaler
func VerifyNoHorizontalPodAutoscaler(channel chan<- error, kind string, namespace string, name string) {
	client, err := k8sClient.getClient()
	if err != nil {
		channel <- err
		return
	}
	hpas, err := client.AutoscalingV1().HorizontalPodAutoscalers(namespace).List(metav1.ListOptions{})
	if err != nil {
		channel <- err
		return
	}
	for _, hpa := range hpas.Items {
		if strings.EqualFold(hpa.Spec.ScaleTargetRef.Kind, kind) && hpa.Spec.ScaleTargetRef.Name == name {
			channel <- fmt.Errorf("Error: %s/%s cannot have a HorizontalPodAutoscaler attached for azp-agent-autoscaler to work", strings.ToLower(kind), name)
			return
		}
	}

	channel <- nil
}

// Scale scales a given Kubernetes resource
func Scale(resource *Workload, replicas int32) error {
	client, err := k8sClient.getClient()
	if err != nil {
		return err
	}

	var getScaleFunc func() (*autoscalingv1.Scale, error)
	var doScaleFunc func(scale *autoscalingv1.Scale) error
	if strings.EqualFold(resource.Kind, "StatefulSet") {
		statefulsets := client.AppsV1().StatefulSets(resource.Namespace)
		getScaleFunc = func() (*autoscalingv1.Scale, error) {
			return statefulsets.GetScale(resource.Name, metav1.GetOptions{})
		}
		doScaleFunc = func(scale *autoscalingv1.Scale) error {
			scale, err := statefulsets.UpdateScale(resource.Name, scale)
			return err
		}
	} else {
		return fmt.Errorf("Resource kind %s is not implemented", resource.Kind)
	}

	scale, err := getScaleFunc()
	if err != nil {
		return err
	}
	if scale.Spec.Replicas == replicas {
		return nil
	}
	scale.Spec.Replicas = replicas
	return doScaleFunc(scale)
}

// GetEnvValue gets an environment variable value
func GetEnvValue(env corev1.EnvVar) (string, error) {
	if env.Value != "" {
		return env.Value, nil
	}
	return "", fmt.Errorf("Error getting value for environment variable %s", env.Name)
}

// Pods is a wrapper around []corev1.Pod to allow returning multiple values in a channel
type Pods struct {
	Pods []corev1.Pod
	Err  error
}

// GetPods gets all pods attached to some workload
func GetPods(channel chan<- Pods, workload *Workload) {
	client, err := k8sClient.getClient()
	if err != nil {
		channel <- Pods{nil, err}
		return
	}

	listOptions := metav1.ListOptions{
		LabelSelector: apimachinery.FormatLabelSelector(workload.PodSelector),
	}
	pods, err := client.CoreV1().Pods(workload.Namespace).List(listOptions)
	if err != nil {
		channel <- Pods{nil, err}
	} else {
		channel <- Pods{pods.Items, nil}
	}
}