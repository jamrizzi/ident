// Bivac v2.0.0 (https://camptocamp.github.io/bivac)
// Copyright (c) 2019 Camptocamp
// Licensed under Apache-2.0 (https://raw.githubusercontent.com/camptocamp/bivac/master/LICENSE)
// Modifications copyright (c) 2019 Jam Risser <jam@codejam.ninja>

package orchestrators

import (
	"bytes"
	"fmt"
	"github.com/codejamninja/volback/pkg/volume"
	"github.com/jinzhu/copier"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// KubernetesConfig stores Kubernetes configuration
type KubernetesConfig struct {
	Namespace           string
	AllNamespaces       bool
	KubeConfig          string
	AgentServiceAccount string
}

// KubernetesOrchestrator implements a container orchestrator for Kubernetes
type KubernetesOrchestrator struct {
	config *KubernetesConfig
	client *kubernetes.Clientset
}

// NewKubernetesOrchestrator creates a Kubernetes client
func NewKubernetesOrchestrator(config *KubernetesConfig) (o *KubernetesOrchestrator, err error) {
	o = &KubernetesOrchestrator{
		config: config,
	}
	c, err := o.getConfig()
	if err != nil {
		err = fmt.Errorf("failed to retrieve config: %s", err)
		return
	}

	o.client, err = kubernetes.NewForConfig(c)
	if err != nil {
		err = fmt.Errorf("failed to create client: %s", err)
		return
	}
	return
}

// GetName returns the orchestrator name
func (*KubernetesOrchestrator) GetName() string {
	return "kubernetes"
}

// GetPath returns the backup path
func (*KubernetesOrchestrator) GetPath(v *volume.Volume) string {
	return v.Namespace
}

// GetVolumes returns the Kubernetes persistent volume claims, inspected and filtered
func (o *KubernetesOrchestrator) GetVolumes(volumeFilters volume.Filters) (volumes []*volume.Volume, err error) {
	// Get namespaces
	namespaces, err := o.getNamespaces()

	for _, namespace := range namespaces {
		pvcs, err := o.client.CoreV1().PersistentVolumeClaims(namespace).List(metav1.ListOptions{})
		if err != nil {
			return nil, err
		}

		for _, pvc := range pvcs.Items {
			if backupString, ok := pvc.Annotations["volback.backup"]; ok {
				if volumeFilters.WhitelistAnnotation {
					if strings.ToLower(backupString) != "true" {
						continue
					}
				} else {
					if strings.ToLower(backupString) == "false" {
						continue
					}
				}
			}
			v := &volume.Volume{
				ID:        string(pvc.UID),
				Labels:    pvc.Labels,
				Logs:      make(map[string]string),
				Name:      pvc.Name,
				Namespace: namespace,
				RepoName:  pvc.Name,
				SubPath:   "",
			}

			containers, _ := o.GetContainersMountingVolume(v)
			containerMap := make(map[string]bool)

			for i := 0; i < len(containers); i++ {
				container := containers[i]
				if _, ok := containerMap[container.Volume.ID]; !ok {
					v = containers[i].Volume
					v.HostBind = containers[i].HostID
					v.Hostname = containers[i].HostID
					v.Mountpoint = containers[i].Path
					if b, _, _ := o.blacklistedVolume(v, volumeFilters); b {
						continue
					}
					volumes = append(volumes, v)
				}
				containerMap[container.Volume.ID] = true
			}
		}
	}
	return
}

// DeployAgent creates a `volback agent` container
func (o *KubernetesOrchestrator) DeployAgent(image string, cmd, envs []string, v *volume.Volume) (success bool, output string, err error) {
	success = false
	kvs := []apiv1.Volume{}
	kvms := []apiv1.VolumeMount{}
	var node string

	var environment []apiv1.EnvVar
	for _, env := range envs {
		splitted := strings.Split(env, "=")
		environment = append(environment, apiv1.EnvVar{
			Name:  splitted[0],
			Value: splitted[1],
		})
	}

	// An additional volume may not be a Persistent Volume (but a ConfigMap for example)
	// Nice feature but the function should be improved
	/*
		additionalVolumes, err := o.getAdditionalVolumes()
		if err != nil {
			err = fmt.Errorf("failed to retrieve additional volumes: %s", err)
			return
		}
	*/

	pvc, err := o.client.CoreV1().PersistentVolumeClaims(v.Namespace).Get(v.Name, metav1.GetOptions{})
	if err != nil {
		err = fmt.Errorf("failed to retrieve PersistentVolumeClaim `%s': %s", v.Name, err)
		return
	}

	for _, am := range pvc.Spec.AccessModes {
		if am == apiv1.ReadWriteOnce {
			node = v.HostBind
		}
	}

	kv := apiv1.Volume{
		Name: v.Name,
		VolumeSource: apiv1.VolumeSource{
			PersistentVolumeClaim: &apiv1.PersistentVolumeClaimVolumeSource{
				ClaimName: v.Name,
				ReadOnly:  false,
			},
		},
	}

	kvs = append(kvs, kv)

	kvm := apiv1.VolumeMount{
		Name:      v.Name,
		ReadOnly:  v.ReadOnly,
		MountPath: v.Mountpoint,
	}

	kvms = append(kvms, kvm)

	/*
		for _, additionalVolume := range additionalVolumes {
			kvs = append(kvs, apiv1.Volume{
				Name: additionalVolume.Name,
				VolumeSource: apiv1.VolumeSource{
					PersistentVolumeClaim: &apiv1.PersistentVolumeClaimVolumeSource{
						ClaimName: additionalVolume.Name,
						ReadOnly:  additionalVolume.ReadOnly,
					},
				},
			})

			kvms = append(kvms, apiv1.VolumeMount{
				Name:      additionalVolume.Name,
				ReadOnly:  additionalVolume.ReadOnly,
				MountPath: additionalVolume.Mountpoint,
			})
		}
	*/

	pod, err := o.client.CoreV1().Pods(v.Namespace).Create(&apiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "volback-agent-",
			Labels: map[string]string{
				"generatedFromPod": os.Getenv("HOSTNAME"),
			},
		},
		Spec: apiv1.PodSpec{
			NodeName:           node,
			RestartPolicy:      "Never",
			Volumes:            kvs,
			ServiceAccountName: o.config.AgentServiceAccount,
			Containers: []apiv1.Container{
				{
					Name:            "volback-agent",
					Image:           image,
					Args:            cmd,
					Env:             environment,
					VolumeMounts:    kvms,
					ImagePullPolicy: apiv1.PullAlways,
				},
			},
		},
	})
	if err != nil {
		err = fmt.Errorf("failed to create agent: %s", err)
		return
	}

	agentName := pod.ObjectMeta.Name
	defer o.DeletePod(agentName, v.Namespace)

	timeout := time.After(60 * 5 * time.Second)
	terminated := false
	for !terminated {
		pod, err := o.client.CoreV1().Pods(v.Namespace).Get(agentName, metav1.GetOptions{})
		if err != nil {
			err = fmt.Errorf("failed to get pod: %s", err)
			return false, "", err
		}

		if pod.Status.Phase == apiv1.PodSucceeded || pod.Status.Phase == apiv1.PodFailed {
			if len(pod.Status.ContainerStatuses) == 0 {
				return false, "", fmt.Errorf("no container found")
			}
			success = true
			terminated = true
		} else if pod.Status.Phase != apiv1.PodRunning {
			select {
			case <-timeout:
				err = fmt.Errorf("failed to start agent: timeout")
				return false, "", err
			default:
				continue
			}
		}
	}

	req := o.client.CoreV1().Pods(v.Namespace).GetLogs(agentName, &apiv1.PodLogOptions{})

	readCloser, err := req.Stream()
	if err != nil {
		err = fmt.Errorf("failed to read logs: %s", err)
		return
	}
	defer readCloser.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(readCloser)

	logs := strings.Split(buf.String(), "\n")
	if len(logs) > 1 {
		output = logs[len(logs)-2]
	}
	return
}

// DeletePod removes pod based on its name
func (o *KubernetesOrchestrator) DeletePod(name, namespace string) {
	err := o.client.CoreV1().Pods(namespace).Delete(name, &metav1.DeleteOptions{})
	if err != nil {
		err = fmt.Errorf("failed to delete agent: %s", err)
	}
	return
}

// GetContainersMountingVolume returns containers mounting a volume
func (o *KubernetesOrchestrator) GetContainersMountingVolume(v *volume.Volume) (containers []*volume.MountedVolume, er error) {
	pods, err := o.client.CoreV1().Pods(v.Namespace).List(metav1.ListOptions{})
	if err != nil {
		err = fmt.Errorf("failed to get pods: %s", err)
		return
	}

	mapVolClaim := make(map[string]string)
	containerMap := make(map[string]*volume.MountedVolume)
	subpathMap := make(map[string]bool)

	for _, pod := range pods.Items {
		for _, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim != nil {
				mapVolClaim[volume.Name] = volume.PersistentVolumeClaim.ClaimName
			}
		}

		for _, container := range pod.Spec.Containers {
			for _, volumeMount := range container.VolumeMounts {
				if c, ok := mapVolClaim[volumeMount.Name]; ok {
					if c == v.Name {
						clonedV := &volume.Volume{}
						copier.Copy(&clonedV, &v)
						splitVolumeID := strings.Split(clonedV.ID, ":")
						if len(splitVolumeID) > 1 {
							clonedV.SubPath = "/" + splitVolumeID[1]
						} else if len(clonedV.SubPath) <= 0 && len(volumeMount.SubPath) > 0 {
							clonedV.ID = v.ID + ":" + volumeMount.SubPath
							clonedV.SubPath = "/" + volumeMount.SubPath
						}
						if len(clonedV.SubPath) > 0 {
							subpathMap[splitVolumeID[0]] = true
							clonedV.RepoName = v.Name + strings.ReplaceAll(clonedV.SubPath, "/", "_")
						}
						if ("/" + volumeMount.SubPath) == clonedV.SubPath {
							mv := &volume.MountedVolume{
								ContainerID: container.Name,
								HostID:      pod.Spec.NodeName,
								Path:        volumeMount.MountPath,
								PodID:       pod.Name,
								Volume:      clonedV,
							}
							containerMap[mv.ContainerID+mv.Volume.ID] = mv
						}
					}
				}
			}
		}
	}
	for _, container := range containerMap {
		if _, ok := subpathMap[strings.Split(container.Volume.ID, ":")[0]]; ok {
			if _, ok := containerMap[strings.Split(container.ContainerID+container.Volume.ID, ":")[0]]; !ok {
				containers = append(containers, container)
			}
		} else {
			containers = append(containers, container)
		}
	}
	return
}

// ContainerExec executes a command in a container
func (o *KubernetesOrchestrator) ContainerExec(mountedVolumes *volume.MountedVolume, command []string) (stdout string, err error) {
	var stdoutput, stderr bytes.Buffer

	config, err := o.getConfig()
	if err != nil {
		err = fmt.Errorf("failed to retrieve Kubernetes config: %s", err)
		return
	}

	req := o.client.Core().RESTClient().Post().
		Resource("pods").
		Name(mountedVolumes.PodID).
		Namespace(o.config.Namespace).
		SubResource("exec").
		Param("container", mountedVolumes.ContainerID)
	req.VersionedParams(&apiv1.PodExecOptions{
		Container: mountedVolumes.ContainerID,
		Command:   command,
		Stdin:     false,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		err = fmt.Errorf("failed to call the API: %s", err)
		return
	}
	err = exec.Stream(remotecommand.StreamOptions{
		Stdin:  nil,
		Stdout: &stdoutput,
		Stderr: &stderr,
		Tty:    false,
	})
	stdout = stdoutput.String()
	return
}

// IsNodeAvailable checks if the node is available to run backups on it
func (o *KubernetesOrchestrator) IsNodeAvailable(hostID string) (ok bool, err error) {
	ok = false

	node, err := o.client.CoreV1().Nodes().Get(hostID, metav1.GetOptions{})
	if err != nil {
		err = fmt.Errorf("failed to retrieve node from the ID `%s': %s", hostID, err)
		return
	}

	for _, condition := range node.Status.Conditions {
		if condition.Type == apiv1.NodeReady && condition.Status == apiv1.ConditionTrue {
			ok = true
		}
	}
	return
}

// RetrieveOrphanAgents returns the list of orphan Volback agents
func (o *KubernetesOrchestrator) RetrieveOrphanAgents() (containers map[string]string, err error) {
	containers = make(map[string]string)
	namespaces, err := o.getNamespaces()
	if err != nil {
		err = fmt.Errorf("failed to get namespaces: %s", err)
		return
	}

	for _, namespace := range namespaces {
		pods, err := o.client.CoreV1().Pods(namespace).List(metav1.ListOptions{})
		if err != nil {
			err = fmt.Errorf("failed to get pods: %s", err)
			return containers, err
		}

		for _, pod := range pods.Items {
			if !strings.HasPrefix(pod.Name, "volback-agent-") {
				continue
			}
			for _, volume := range pod.Spec.Volumes {
				if volume.PersistentVolumeClaim != nil {
					containers[volume.Name] = pod.Name
				}
			}
		}
	}

	return
}

// AttachOrphanAgent connects to a running agent and wait for the end of the backup proccess
func (o *KubernetesOrchestrator) AttachOrphanAgent(containerID, namespace string) (success bool, output string, err error) {
	_, err = o.client.CoreV1().Pods(namespace).Get(containerID, metav1.GetOptions{})
	if err != nil {
		err = fmt.Errorf("failed to get pod: %s", err)
		return false, "", err
	}
	defer o.DeletePod(containerID, namespace)

	timeout := time.After(60 * time.Second)
	terminated := false
	for !terminated {
		pod, err := o.client.CoreV1().Pods(namespace).Get(containerID, metav1.GetOptions{})
		if err != nil {
			err = fmt.Errorf("failed to get pod: %s", err)
			return false, "", err
		}

		if pod.Status.Phase == apiv1.PodSucceeded || pod.Status.Phase == apiv1.PodFailed {
			if len(pod.Status.ContainerStatuses) == 0 {
				return false, "", fmt.Errorf("no container found")
			}
			success = true
			terminated = true
		} else if pod.Status.Phase != apiv1.PodRunning {
			select {
			case <-timeout:
				err = fmt.Errorf("failed to start agent: timeout")
				return false, "", err
			default:
				continue
			}
		}
	}

	req := o.client.CoreV1().Pods(namespace).GetLogs(containerID, &apiv1.PodLogOptions{})

	readCloser, err := req.Stream()
	if err != nil {
		err = fmt.Errorf("failed to read logs: %s", err)
		return
	}
	defer readCloser.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(readCloser)
	logs := strings.Split(buf.String(), "\n")
	if len(logs) > 1 {
		output = logs[len(logs)-2]
	}

	return
}

func (o *KubernetesOrchestrator) blacklistedVolume(vol *volume.Volume, volumeFilters volume.Filters) (bool, string, string) {
	if utf8.RuneCountInString(vol.Name) == 64 || utf8.RuneCountInString(vol.Name) == 0 {
		return true, "unnamed", ""
	}

	// Check labels
	if ignored, ok := vol.Labels["volback.ignore"]; ok && ignored == "true" {
		return true, "ignored", "volume config"
	}

	if strings.Contains(vol.Name, "/") {
		return true, "unnamed", "path"
	}
	// Use whitelist if defined
	if l := volumeFilters.Whitelist; len(l) > 0 && l[0] != "" {
		sort.Strings(l)
		i := sort.SearchStrings(l, vol.Name)
		if i < len(l) && l[i] == vol.Name {
			return false, "", ""
		}
		return true, "blacklisted", "whitelist config"
	}

	i := sort.SearchStrings(volumeFilters.Blacklist, vol.Name)
	if i < len(volumeFilters.Blacklist) && volumeFilters.Blacklist[i] == vol.Name {
		return true, "blacklisted", "blacklist config"
	}
	return false, "", ""
}

// DetectKubernetes returns true if Volback is running on the orchestrator Kubernetes
func DetectKubernetes() bool {
	_, err := rest.InClusterConfig()
	if err != nil {
		return false
	}
	return true
}

func (o *KubernetesOrchestrator) getConfig() (config *rest.Config, err error) {
	if o.config.KubeConfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", o.config.KubeConfig)
	} else {
		kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			&clientcmd.ConfigOverrides{},
		)

		if o.config.Namespace == "" {
			o.config.Namespace, _, err = kubeconfig.Namespace()
			if err != nil {
				err = fmt.Errorf("failed to retrieve namespace from the cluster config: %s", err)
				return
			}
		}
		config, err = rest.InClusterConfig()
	}
	return
}

func (o *KubernetesOrchestrator) getNamespaces() (namespaces []string, err error) {
	if o.config.AllNamespaces == true {
		nms, err := o.client.CoreV1().Namespaces().List(metav1.ListOptions{})
		if err != nil {
			err = fmt.Errorf("failed to retrieve the list of namespaces: %s", err)
			return []string{}, err
		}
		for _, namespace := range nms.Items {
			namespaces = append(namespaces, namespace.Name)
		}
	} else {
		namespaces = append(namespaces, o.config.Namespace)
	}
	return
}

func (o *KubernetesOrchestrator) getAdditionalVolumes() (mounts []*volume.Volume, err error) {
	mounts = []*volume.Volume{}

	managerHostname, err := os.Hostname()
	if err != nil {
		err = fmt.Errorf("failed to retrieve manager's hostname: %s", err)
		return
	}

	// get the namespace
	kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	)
	namespace, _, err := kubeconfig.Namespace()
	if err != nil {
		err = fmt.Errorf("failed to get namespace: %v", err)
		return
	}

	managerPod, err := o.client.CoreV1().Pods(namespace).Get(managerHostname, metav1.GetOptions{})
	if err != nil {
		err = fmt.Errorf("failed to retrieve manager's pod: %s", err)
		return
	}

	for _, v := range managerPod.Spec.Containers[0].VolumeMounts {
		mounts = append(mounts, &volume.Volume{
			Name:       v.Name,
			ReadOnly:   v.ReadOnly,
			Mountpoint: v.MountPath,
		})
	}
	return
}
