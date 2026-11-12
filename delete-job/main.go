package main

import (
	"errors"
	"log"
	"os"
	"time"

	"k8s.io/client-go/kubernetes"

	"context"

	"github.com/jackc/pgx/v5"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func loadConfig(ctx context.Context, roleARN, externalID string, awsRegion string) (aws.Config, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(awsRegion))
	if err != nil {
		return cfg, err
	}
	stsClient := sts.NewFromConfig(cfg)
	provider := stscreds.NewAssumeRoleProvider(stsClient, roleARN, func(o *stscreds.AssumeRoleOptions) {
		o.ExternalID = aws.String(externalID)
	})
	cfg.Credentials = aws.NewCredentialsCache(provider)
	return cfg, nil
}

func getInstanceIDByPublicIP(ctx context.Context, client *ec2.Client, publicIP string) (string, error) {
	input := &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{
			{
				Name:   aws.String("ip-address"),
				Values: []string{publicIP},
			},
		},
	}

	result, err := client.DescribeInstances(ctx, input)
	if err != nil {
		return "", err
	}

	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.InstanceId != nil {
				return *instance.InstanceId, nil
			}
		}
	}

	return "", errors.New("no EC2 instance found with that public IP")
}

func deleteKey(ctx context.Context, client *ec2.Client, keyname string) error {
	_, err := client.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{
		KeyName: &keyname,
	})
	return err
}

func isAPIError(err error, code string) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == code
}

func deleteSG(ctx context.Context, client *ec2.Client, sgName string) error {
	_, err := client.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
		GroupName: aws.String(sgName),
	})
	if isAPIError(err, "InvalidGroup.NotFound") {
		return nil
	}
	return err
}

func deleteEc2(ctx context.Context, client *ec2.Client, ip string) error {
	instanceID, err := getInstanceIDByPublicIP(ctx, client, ip)
	if err != nil {
		return err
	}
	_, err = client.TerminateInstances(ctx, &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	})
	return err
}

func deleteEveryRow(ctx context.Context, uuid string, randomCode string, db *pgx.Conn) error {
	deleteStr := `DELETE FROM clusterinfo WHERE user_id = $1 AND random_code = $2`
	_, err := db.Exec(ctx, deleteStr, uuid, randomCode)
	if err != nil {
		return err
	}
	return nil
}

func main() {
	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	ctx := context.Background()
	defer cancel()
	connStr := os.Getenv("DATABASE_URL")
	conn, err := pgx.Connect(startCtx, connStr)
	if err != nil {
		panic(err)
	}
	defer conn.Close(ctx)
	uuid := os.Getenv("UUID")
	if uuid == "" {
		log.Fatalln("Failed To get UUID")
	}
	randomCode := os.Getenv("RANDOMCODE")
	if randomCode == "" {
		log.Fatalln("Failed To get Random Code")
	}
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		log.Fatalln("Failed To get Namespace")
	}
	if err := deleteEveryRow(ctx, uuid, randomCode, conn); err != nil {
		log.Println("Failed To Clear Database ")
	}
	clusterName := os.Getenv("Cluster_Name")
	if clusterName == "" {
		log.Fatalln("Failed To get Cluster Name")
	}
	restCfg := ctrl.GetConfigOrDie()
	k8client := kubernetes.NewForConfigOrDie(restCfg)
	cli := k8client.CoreV1()
	cm, err := cli.ConfigMaps(namespace).Get(ctx, "alivenodeips", metav1.GetOptions{})
	if err != nil {
		log.Fatalln("Failed To get Config Map ", err)
	}
	data := cm.Data
	cm.Data = map[string]string{}
	_, err = cli.ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		log.Println("Failed To Modify Config Map ", err)
	}
	if err := cli.Pods(namespace).Delete(ctx, clusterName, metav1.DeleteOptions{}); err != nil {
		log.Println("Failed To Delete Dispatch Pod ", err)
	}
	roleARN := os.Getenv("ROLEARN")

	if roleARN == "" {
		for _, value := range data {
			err := cli.Pods(namespace).Delete(ctx, value, metav1.DeleteOptions{})
			if err != nil {
				log.Println("Failed To Delete Pod ", err)
			}
		}
	} else {
		externalID := os.Getenv("EXTERNALID")
		if externalID == "" {
			log.Fatalln("Failed To Get External Id")
		}
		awsRegion := os.Getenv("AWSREGION")
		if awsRegion == "" {
			log.Fatalln("Failed To Get AWS Region ")
		}
		cfg, err := loadConfig(ctx, roleARN, externalID, awsRegion)
		if err != nil {
			log.Fatalln("Failed To Load AWS Config ", err)
		}
		client := ec2.NewFromConfig(cfg)
		keyname := "k8-cluster-key-" + randomCode
		if err := deleteKey(ctx, client, keyname); err != nil {
			log.Println("Failed To Delete Key ", err)
		}
		for key, value := range data {
			err := cli.Pods(namespace).Delete(ctx, value, metav1.DeleteOptions{})
			if err != nil {
				log.Println("Failed To Delete Pod ", err)
			}
			err = deleteEc2(ctx, client, key)
			if err != nil {
				log.Println("Failed To Delete Instance ", err)
			}
		}
		time.Sleep(time.Minute * 2)
		sgName := "Kubeadm-Security-Group-" + randomCode
		if err := deleteSG(ctx, client, sgName); err != nil {
			log.Println("Failed To Delete Security Group ", err)
		}
	}
}
