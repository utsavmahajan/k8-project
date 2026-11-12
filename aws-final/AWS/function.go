package aws

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// hasCode reports whether err is an AWS API error with one of the given codes.
func hasCode(err error, codes ...string) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		for _, c := range codes {
			if apiErr.ErrorCode() == c {
				return true
			}
		}
	}
	return false
}

func LoadConfig(ctx context.Context, roleARN, externalID string, awsRegion string) (aws.Config, error) {
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

func getAMI(awsRegion string) string {
	ami := map[string]string{
		"us-east-1": "ami-02a9cdd4e98bc3c78",
	}
	return ami[awsRegion]
}

// CreateSSHKey creates the key pair. If a key with the same name already
// exists (e.g. left over from a crashed run) it is deleted and recreated,
// because AWS only returns the private key material at creation time.
func CreateSSHKey(ctx context.Context, client *ec2.Client, randstr string) (*ec2.CreateKeyPairOutput, error) {
	keyName := "k8-cluster-key-" + randstr
	input := &ec2.CreateKeyPairInput{
		KeyName: aws.String(keyName),
		KeyType: ec2types.KeyTypeEd25519,
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeKeyPair,
			Tags:         []ec2types.Tag{{Key: aws.String("managed-by"), Value: aws.String("myapp")}},
		}},
	}

	keyPair, err := client.CreateKeyPair(ctx, input)
	if err != nil && hasCode(err, "InvalidKeyPair.Duplicate") {
		log.Printf("key pair %s already exists, deleting and recreating", keyName)
		if _, derr := client.DeleteKeyPair(ctx, &ec2.DeleteKeyPairInput{KeyName: aws.String(keyName)}); derr != nil {
			return nil, fmt.Errorf("delete stale key pair: %w", derr)
		}
		keyPair, err = client.CreateKeyPair(ctx, input)
	}
	if err != nil {
		return nil, err
	}

	// Best effort local copy; the key is stored in a k8s Secret by the caller,
	// so a read-only filesystem must not fail the whole run.
	pemFile := fmt.Sprintf("%s.pem", keyName)
	if werr := os.WriteFile(pemFile, []byte(*keyPair.KeyMaterial), 0600); werr != nil {
		log.Printf("warning: could not write %s: %v", pemFile, werr)
	}
	return keyPair, nil
}

// CreateSecGroup is idempotent: an existing group is reused and already
// present ingress rules are ignored.
func CreateSecGroup(ctx context.Context, client *ec2.Client, randstr string) error {
	description := "Rules Required For K8"
	groupName := "Kubeadm-Security-Group-" + randstr

	var groupID *string
	sg, err := client.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		Description: aws.String(description),
		GroupName:   aws.String(groupName),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSecurityGroup,
			Tags:         []ec2types.Tag{{Key: aws.String("managed-by"), Value: aws.String("myapp")}},
		}},
	})
	if err != nil {
		if !hasCode(err, "InvalidGroup.Duplicate") {
			return fmt.Errorf("create security group: %w", err)
		}
		log.Printf("security group %s already exists, reusing", groupName)
		desc, derr := client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
			Filters: []ec2types.Filter{{Name: aws.String("group-name"), Values: []string{groupName}}},
		})
		if derr != nil {
			return fmt.Errorf("describe existing security group: %w", derr)
		}
		if len(desc.SecurityGroups) == 0 {
			return fmt.Errorf("security group %s reported duplicate but was not found", groupName)
		}
		groupID = desc.SecurityGroups[0].GroupId
	} else {
		groupID = sg.GroupId
	}

	tcp := func(from, to int32) ec2types.IpPermission {
		return ec2types.IpPermission{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(from),
			ToPort:     aws.Int32(to),
			IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		}
	}

	_, err = client.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
		GroupId: groupID,
		IpPermissions: []ec2types.IpPermission{
			{
				IpProtocol:       aws.String("-1"),
				UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: groupID}},
			},
			tcp(22, 22),
			tcp(80, 80),
			tcp(443, 443),
			tcp(30000, 32767),
		},
	})
	if err != nil && !hasCode(err, "InvalidPermission.Duplicate") {
		return fmt.Errorf("authorize security group ingress: %w", err)
	}
	return nil
}

func CreateEC2(ctx context.Context, client *ec2.Client, randstr string, keyName *string, awsRegion string, instanceType string, storage int, Name string, value string) (error, string) {
	ami := getAMI(awsRegion)
	if ami == "" {
		return fmt.Errorf("no AMI configured for region %q", awsRegion), ""
	}
	groupName := "Kubeadm-Security-Group-" + randstr
	instanceName := value + "-" + randstr
	runResult, err := client.RunInstances(ctx, &ec2.RunInstancesInput{
		ImageId:      aws.String(ami),
		InstanceType: ec2types.InstanceType(instanceType),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		KeyName:      keyName,
		BlockDeviceMappings: []ec2types.BlockDeviceMapping{
			{
				DeviceName: aws.String("/dev/sda1"),
				Ebs: &ec2types.EbsBlockDevice{
					VolumeSize:          aws.Int32(int32(storage)),
					VolumeType:          ec2types.VolumeTypeGp3,
					DeleteOnTermination: aws.Bool(true),
				},
			},
		},
		SecurityGroups: []string{groupName},
		TagSpecifications: []ec2types.TagSpecification{
			{
				ResourceType: ec2types.ResourceTypeInstance,
				Tags: []ec2types.Tag{
					{Key: aws.String("managed-by"), Value: aws.String("myapp")},
					{Key: aws.String(Name), Value: aws.String(value)},
					{Key: aws.String("Name"), Value: aws.String(instanceName)},
				},
			},
			{
				ResourceType: ec2types.ResourceTypeVolume,
				Tags: []ec2types.Tag{
					{Key: aws.String("managed-by"), Value: aws.String("myapp")},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("run instances: %w", err), ""
	}
	if len(runResult.Instances) == 0 || runResult.Instances[0].InstanceId == nil {
		return fmt.Errorf("RunInstances returned no instances"), ""
	}
	instanceId := *runResult.Instances[0].InstanceId

	runWaiter := ec2.NewInstanceRunningWaiter(client)
	described, err := runWaiter.WaitForOutput(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceId},
	}, 3*time.Minute)
	if err != nil {
		return fmt.Errorf("waiting for instance %s: %w", instanceId, err), ""
	}

	var inst *ec2types.Instance
	for _, r := range described.Reservations {
		for i := range r.Instances {
			if r.Instances[i].InstanceId != nil && *r.Instances[i].InstanceId == instanceId {
				inst = &r.Instances[i]
			}
		}
	}
	if inst == nil {
		return fmt.Errorf("instance %s not found in describe output", instanceId), ""
	}

	publicIP := aws.ToString(inst.PublicIpAddress)
	if publicIP == "" {
		log.Printf("warning: instance %s has no public IP (subnet may not auto-assign one)", instanceId)
	}
	return nil, publicIP
}

func CreatetestEC2(ctx context.Context, client *ec2.Client, randstr string, keyName *string, awsRegion string, instanceType string, storage int, Name string, value string) (error, string) {
    ami := getAMI(awsRegion)
    GroupName := "Kubeadm-Security-Group-" + randstr

    runResult, err := client.RunInstances(ctx, &ec2.RunInstancesInput{
        ImageId:      aws.String(ami),
        InstanceType: ec2types.InstanceType(instanceType),
        MinCount:     aws.Int32(1),
        MaxCount:     aws.Int32(1),
        KeyName:      keyName,
        BlockDeviceMappings: []ec2types.BlockDeviceMapping{
            {
                DeviceName: aws.String("/dev/sda1"),
                Ebs: &ec2types.EbsBlockDevice{
                    VolumeSize:          aws.Int32(int32(storage)),
                    VolumeType:          ec2types.VolumeTypeGp3,
                    DeleteOnTermination: aws.Bool(true),
                },
            },
        },
        SecurityGroups: []string{GroupName},
        TagSpecifications: []ec2types.TagSpecification{
            {
                ResourceType: ec2types.ResourceTypeInstance,
                Tags: []ec2types.Tag{
                    {Key: aws.String("managed-by"), Value: aws.String("myapp")},
                    {Key: aws.String(Name), Value: aws.String(value)},
                },
            },
            {
                ResourceType: ec2types.ResourceTypeVolume,
                Tags: []ec2types.Tag{
                    {Key: aws.String("managed-by"), Value: aws.String("myapp")},
                },
            },
        },
    })
    if err != nil {
        return err, ""
    }
    if len(runResult.Instances) == 0 || runResult.Instances[0].InstanceId == nil {
        return fmt.Errorf("RunInstances returned no instances"), ""
    }

    instanceId := *runResult.Instances[0].InstanceId

    runWaiter := ec2.NewInstanceRunningWaiter(client)
    described, err := runWaiter.WaitForOutput(ctx, &ec2.DescribeInstancesInput{
        InstanceIds: []string{instanceId},
    }, 3*time.Minute)
    if err != nil {
        return err, ""
    }
    var inst *ec2types.Instance
    for _, r := range described.Reservations {
        for i := range r.Instances {
            if r.Instances[i].InstanceId != nil && *r.Instances[i].InstanceId == instanceId {
                inst = &r.Instances[i]
            }
        }
    }
    if inst == nil {
        return fmt.Errorf("instance %s not found in describe output", instanceId), ""
    }

    publicIP := aws.ToString(inst.PublicIpAddress) 

    if publicIP == "" {
        log.Printf("warning: instance %s has no public IP (subnet may not auto-assign one)", instanceId)
    }

    return nil, publicIP
}