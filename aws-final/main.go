package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	dispatchv1 "github.com/utsavmahajan/ec2-launchpad/api/v1"
	AWS "github.com/utsavmahajan/k8-project/ec2-launcher/AWS"
	k8 "github.com/utsavmahajan/k8-project/ec2-launcher/kubernetes"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	kcli "sigs.k8s.io/controller-runtime/pkg/client"
)

func getENV(envName string) (string, error) {
	data, err := os.ReadFile("/values/" + envName)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// updateStatus re-fetches the latest CR on every attempt so that a conflict
// can actually resolve, mutates it, writes the status, and refreshes `obj`.
func updateStatus(ctx context.Context, c kcli.Client, key kcli.ObjectKey,
	obj *dispatchv1.ClusterInit, mutate func(*dispatchv1.ClusterInit)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest dispatchv1.ClusterInit
		if err := c.Get(ctx, key, &latest); err != nil {
			return err
		}
		mutate(&latest)
		if err := c.Status().Update(ctx, &latest); err != nil {
			return err
		}
		*obj = latest
		return nil
	})
}

func main() {
	ctx := context.Background()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		log.Fatalln("Failed To Load Default Schemes ", err)
	}
	if err := dispatchv1.AddToScheme(scheme); err != nil {
		log.Fatalln("Failed To Load Custom Operator ", err)
	}

	restCfg := ctrl.GetConfigOrDie()
	c, err := kcli.New(restCfg, kcli.Options{Scheme: scheme})
	if err != nil {
		log.Fatalln("Failed To Load Default Context ", err)
	}
	k8client := kubernetes.NewForConfigOrDie(restCfg)
	cli := k8client.CoreV1()

	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		log.Fatalln("Failed To Get Namespace")
	}
	randStr := strings.ToLower(os.Getenv("RANDSTR"))
	if randStr == "" {
		log.Fatalln("Failed To Get Random CODE")
	}
	roleARN := os.Getenv("ROLEARN")
	if roleARN == "" {
		log.Fatalln("Failed To Get Role ARN")
	}
	externalID := os.Getenv("EXTERNALID")
	if externalID == "" {
		log.Fatalln("Failed To Get External ID")
	}
	awsRegion := os.Getenv("AWSREGION")
	if awsRegion == "" {
		log.Fatalln("Failed To Get AWS REGION")
	}
	Name := os.Getenv("CLUSTER_NAME")
	if Name == "" {
		log.Fatalln("Failed To Get Cluster Name")
	}

	cfg, err := AWS.LoadConfig(ctx, roleARN, externalID, awsRegion)
	if err != nil {
		log.Fatalln("Failed To Load AWS Config ", err)
	}
	client := ec2.NewFromConfig(cfg)

	controlNodeType, err := getENV("Control_Node_type.txt")
	if err != nil || controlNodeType == "" {
		log.Println("Failed To Get Control Node Type", err)
		controlNodeType = "c7i-flex.large"
	}
	storageStr, err := getENV("storage.txt")
	if err != nil || storageStr == "" {
		log.Println("Failed To Get Storage Value", err)
		storageStr = "8"
	}
	storage, err := strconv.Atoi(storageStr)
	if err != nil || storage < 8 {
		log.Println("Failed To get Storage in integer (or < 8), using 8", err)
		storage = 8
	}
	helm, err := getENV("helm.txt")
	if err != nil {
		log.Println("Failed To get Helm Value ", err)
		helm = "true"
	}
	crictl, err := getENV("crictl.txt")
	if err != nil {
		log.Println("Failed To get crictl Value ", err)
		crictl = "false"
	}
	addons, err := getENV("addons.txt")
	if err != nil {
		log.Println("Failed To get addons Value ", err)
		addons = ""
	}
	CNI, err := getENV("CNI.txt")
	if err != nil {
		log.Println("Failed To get CNI Value ", err)
		CNI = "flannel"
	}
	ingress, err := getENV("ingress.txt")
	if err != nil {
		log.Println("Failed To get ingress Value ", err)
		ingress = ""
	}
	gateway, err := getENV("gateway.txt")
	if err != nil {
		log.Println("Failed To get gateway Value ", err)
		gateway = ""
	}

	var got dispatchv1.ClusterInit
	key := kcli.ObjectKey{Namespace: namespace, Name: Name}
	if err = c.Get(ctx, key, &got); err != nil {
		log.Fatalln("Failed To Get the Custom Resource ", err)
	}

	// ---------------- Security group ----------------
	if !got.Status.SgCreated {
		if err = AWS.CreateSecGroup(ctx, client, randStr); err != nil {
			log.Fatalln("Failed to create Security Group ", err)
		}
		if err = updateStatus(ctx, c, key, &got, func(ci *dispatchv1.ClusterInit) {
			ci.Status.SgCreated = true
		}); err != nil {
			log.Fatalln("Failed To Update Status (SgCreated) ", err)
		}
		log.Println("Security group created and status saved")
	}

	// ---------------- SSH key ----------------
	if !got.Status.SshKeyCreated {
		keyPair, err := AWS.CreateSSHKey(ctx, client, randStr)
		if err != nil {
			log.Fatalln("Failed to create SSH key ", err)
		}
		if err = k8.CreateSecret(ctx, cli, *keyPair.KeyMaterial, namespace); err != nil {
			log.Fatalln("Failed To Create The Ansible Secret ", err)
		}
		createdKeyName := *keyPair.KeyName
		if err = updateStatus(ctx, c, key, &got, func(ci *dispatchv1.ClusterInit) {
			ci.Status.SshKeyCreated = true
			ci.Status.SshKeyName = createdKeyName
		}); err != nil {
			log.Fatalln("Failed To Update Status (SshKeyCreated) ", err)
		}
		log.Println("SSH key created and status saved")
	}
	// Read AFTER the key step so it is never empty.
	keyName := got.Status.SshKeyName
	if keyName == "" {
		keyName = "k8-cluster-key-" + randStr
	}

	controlNodePublicIP := ""
	if !got.Status.ControlNodeCreated {
		var ip string
		err, ip = AWS.CreateEC2(ctx, client, randStr, &keyName, awsRegion, controlNodeType, storage, "Type", "Control-Node")
		if err != nil {
			log.Fatalln("Failed To Create Control Node ", err)
		}
		controlNodePublicIP = ip

		// Persist immediately so a restart never launches a second control node.
		if err = updateStatus(ctx, c, key, &got, func(ci *dispatchv1.ClusterInit) {
			ci.Status.ControlNodeCreated = true
			ci.Status.ControlNodePublicIP = controlNodePublicIP
		}); err != nil {
			log.Fatalln("Failed To Update Status (ControlNodeCreated) ", err)
		}

		if err = k8.UpdateCM(ctx, cli, "controlNodePublicIP", controlNodePublicIP, namespace, Name); err != nil {
			log.Println("Failed To Add Control Node Public IP in CM ", err)
		}
		if err = k8.CreatePod(ctx, cli, namespace, "control-plane", controlNodePublicIP, "control-plane"); err != nil {
			log.Println("Failed To create Watcher POD for Control-Node ", err)
		}

		envlist := []v1.EnvVar{
			{Name: "CONTROL_NODE_IP", Value: controlNodePublicIP},
			{Name: "ROLE", Value: "control-plane"},
			{Name: "HELM", Value: helm},
			{Name: "CRICTL", Value: crictl},
			{Name: "CNI", Value: CNI},
			{Name: "ADDONS", Value: addons},
			{Name: "INGRESS", Value: ingress},
			{Name: "GATEWAY", Value: gateway},
		}
		if err = k8.CreateJob(ctx, k8client, namespace, envlist); err != nil {
			log.Fatalln("Failed To initialize Cluster ", err)
		}
		if err = k8.CreateCM(ctx, cli, namespace, "alivenodeips"); err != nil {
			log.Println("Failed To Create alivenodeips ", err)
		}
		time.Sleep(4 * time.Minute)
	} else {
		controlNodePublicIP = got.Status.ControlNodePublicIP
	}

	// ---------------- Worker nodes ----------------
	for {
		runningWorkerNodes, err := k8.NoofRunningWorkers(ctx, cli, namespace)
		if err != nil {
			log.Fatalln("Failed To get No of Running Worker Pods ", err)
		}

		instanceList, err := cli.ConfigMaps(namespace).Get(ctx, "instancetypes", metav1.GetOptions{})
		if err != nil {
			log.Println("Failed To get instancetypes CM ", err)
			time.Sleep(30 * time.Second)
			continue
		}

		if len(instanceList.Data) <= runningWorkerNodes {
			time.Sleep(30 * time.Second)
			continue
		}

		var newWorkerIPs []string

		for nodeName, instanceType := range instanceList.Data {
			label := "name=" + nodeName
			present, err := k8.IsPresent(ctx, cli, namespace, label)
			if err != nil {
				log.Fatalln("Something went Wrong ", err)
			}
			if present {
				continue
			}

			err, workerIP := AWS.CreateEC2(ctx, client, randStr, &keyName, awsRegion, instanceType, storage, "Type", "worker-plane")
			if err != nil {
				log.Println("Failed To Create a Worker Node ", err)
				continue
			}

			if err = k8.CreatePod(ctx, cli, namespace, "worker-plane", workerIP, nodeName); err != nil {
				log.Println("Failed To create Watcher POD for Worker-Node ", err)
			}

			newWorkerIPs = append(newWorkerIPs, workerIP)
		}

		// Nothing launched this pass (all present, or all launches failed).
		if len(newWorkerIPs) == 0 {
			time.Sleep(30 * time.Second)
			continue
		}

		// One Job handles all the workers launched in this pass.
		envlist := []v1.EnvVar{
			{Name: "WORKER_NODE_IPS", Value: strings.Join(newWorkerIPs, ",")},
			{Name: "CONTROL_NODE_IP", Value: controlNodePublicIP},
			{Name: "ROLE", Value: "worker-plane"},
			{Name: "CRICTL", Value: crictl},
		}
		if err = k8.CreateJob(ctx, k8client, namespace, envlist); err != nil {
			log.Fatalln("Failed To initialize Cluster ", err)
		}

		time.Sleep(5 * time.Minute)
	}
}
