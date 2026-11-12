package kubernetes

import (
	"context"
	"log"

	"os"

	batch "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

func UpdateCM(ctx context.Context, cli corev1.CoreV1Interface, key string, value string, Namespace string, Name string) error {
	cm, err := cli.ConfigMaps(Namespace).Get(ctx, Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[key] = value
	_, err = cli.ConfigMaps(Namespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	return nil
}

func CreateCM(ctx context.Context, cli corev1.CoreV1Interface, Namespace string, Name string) error {
	cmDefinition := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      Name,
			Namespace: Namespace,
		},
		Data: map[string]string{},
	}
	_, err := cli.ConfigMaps(Namespace).Create(ctx, cmDefinition, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	return nil
}

func CreateSecret(ctx context.Context, cli corev1.CoreV1Interface, data string, Namespace string) error {
	secretDefinition := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sshkey",
			Namespace: Namespace,
		},
		Type: v1.SecretTypeOpaque,
		Data: map[string][]byte{
			"ansible-secret.pem": []byte(data),
		},
	}
	_, err := cli.Secrets(Namespace).Create(ctx, secretDefinition, metav1.CreateOptions{})
	return err
}

func NoofRunningWorkers(ctx context.Context, cli corev1.CoreV1Interface, Namespace string) (int, error) {
	listOptions := metav1.ListOptions{
		LabelSelector: "ROLES=worker-plane",
	}
	podList, err := cli.Pods(Namespace).List(ctx, listOptions)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, pod := range podList.Items {
		if pod.Status.Phase == v1.PodRunning {
			count++
		}
	}
	return count, nil
}

func IsPresent(ctx context.Context, cli corev1.CoreV1Interface, Namespace string, label string) (bool, error) {
	listOptions := metav1.ListOptions{
		LabelSelector: label,
	}

	pods, err := cli.Pods(Namespace).List(ctx, listOptions)
	if err != nil {
		return false, err
	}

	for _, pod := range pods.Items {
		if pod.Status.Phase == v1.PodRunning {
			return true, nil
		}
	}

	return false, nil
}

func GetIPCMData(ctx context.Context, cli corev1.CoreV1Interface, Namespace string) map[string]string {
	cm, err := cli.ConfigMaps(Namespace).Get(ctx, "publicips", metav1.GetOptions{})
	if err != nil {
		log.Println("Failed To get Public IP's from configMap ", err)
		return map[string]string{}
	}
	return cm.Data
}

func CreateJob(ctx context.Context, client *kubernetes.Clientset, Namespace string, envlist []v1.EnvVar) error {
	jobcli := client.BatchV1().Jobs(Namespace)
	backoffLimit := int32(1)
	secretDefaultMode := int32(0400)
	runasUser := int64(0)
	jobDefinition := &batch.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "clusterinitjob",
			Namespace:    Namespace,
		},
		Spec: batch.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: v1.PodTemplateSpec{
				Spec: v1.PodSpec{
					SecurityContext: &v1.PodSecurityContext{
						RunAsUser: &runasUser,
					},
					Volumes: []v1.Volume{
						{
							Name: "sshkey",
							VolumeSource: v1.VolumeSource{
								Secret: &v1.SecretVolumeSource{
									SecretName:  "sshkey",
									DefaultMode: &secretDefaultMode,
								},
							},
						},
						{
							Name:     "v1",
							EmptyDir: &v1.EmptyDirVolumeSource{},
						},
					},
					InitContainers: []v1.Container{
						{
							Name:  "gitosmodule",
							Image: "utsav5/gitosmodule:v2",
							Env:   envlist,
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "v1",
									MountPath: "/work/data",
								},
							},
						},
					},
					Containers: []v1.Container{
						{
							Name:       "ansiblejob",
							Image:      "willhallonline/ansible:latest",
							WorkingDir: "/",
							Command:    []string{"sh", "/data/scripts/script.sh"},
							VolumeMounts: []v1.VolumeMount{
								{
									Name:      "v1",
									MountPath: "/data",
								},
								{
									Name:      "sshkey",
									MountPath: "/ansible/ssh/id_rsa",
									ReadOnly:  true,
								},
							},
							Env: []v1.EnvVar{
								{
									Name:  "ANSIBLE_HOST_KEY_CHECKING",
									Value: "False",
								},
								{
									Name:  "ANSIBLE_TIMEOUT",
									Value: "60",
								},
								{
									Name:  "ANSIBLE_PIPELINING",
									Value: "True",
								},
							},
						},
					},
					RestartPolicy: v1.RestartPolicyNever,
				},
			},
		},
	}

	if _, err := jobcli.Create(ctx, jobDefinition, metav1.CreateOptions{}); err != nil {
		return err
	}
	return nil
}

func CreatePod(ctx context.Context, cli corev1.CoreV1Interface, Namespace string, role string, ip string, name string) error {
	podCli := cli.Pods(Namespace)

	podDefinition := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: role + "-",
			Namespace:    Namespace,
			Labels:       map[string]string{"ROLES": role, "name": name},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:  "ping-pod",
					Image: "utsav5/ping-ec2-pod:v6",
					Env: []v1.EnvVar{
						{
							Name:  "DATABASE_URL",
							Value: os.Getenv("DATABASE_URL"),
						},
						{
							Name:  "NAMESPACE",
							Value: os.Getenv("NAMESPACE"),
						},
						{
							Name:  "DISPATCH_NAME",
							Value: os.Getenv("DISPATCH_NAME"),
						},
						{
							Name:  "UUID",
							Value: os.Getenv("UUID"),
						},
						{
							Name:  "RANDSTR",
							Value: os.Getenv("RANDSTR"),
						},
						{
							Name:  "ROLE",
							Value: role,
						},
						{
							Name:  "IP",
							Value: ip,
						},
						{
							Name: "POD_NAME",
							ValueFrom: &v1.EnvVarSource{
								FieldRef: &v1.ObjectFieldSelector{FieldPath: "metadata.name"},
							},
						},
					},
				},
			},
		},
	}
	_, err := podCli.Create(ctx, podDefinition, metav1.CreateOptions{})
	return err
}
