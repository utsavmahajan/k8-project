package main

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"time"

	dispatchv1 "github.com/utsavmahajan/ec2-launchpad/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	"context"

	"github.com/jackc/pgx/v5"
	ctrl "sigs.k8s.io/controller-runtime"
	kcli "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func isAlive(ip string, timeout time.Duration) bool {
	addr := net.JoinHostPort(ip, "22")
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func deleteEveryRow(ctx context.Context, uuid string, randomCode string, db *pgx.Conn) error {
	deleteStr := `DELETE FROM clusterinfo WHERE user_id = $1 AND random_code = $2`
	_, err := db.Exec(ctx, deleteStr, uuid, randomCode)
	if err != nil {
		return err
	}
	return nil
}

func insertRow(ctx context.Context, uuid string, randomCode string, db *pgx.Conn, ip string, role string) error {
	insertStr := "INSERT INTO clusterinfo(user_id,random_code,instance_public_ip,role) VALUES($1,$2,$3,$4)"
	_, err := db.Exec(ctx, insertStr, uuid, randomCode, ip, role)
	if err != nil {
		return err
	}
	return nil
}

func deleteARow(ctx context.Context, uuid string, randomCode string, db *pgx.Conn, ip string) error {
	deleteStr := `DELETE FROM clusterinfo WHERE user_id = $1 AND random_code = $2 AND instance_public_ip = $3`
	_, err := db.Exec(ctx, deleteStr, uuid, randomCode, ip)
	if err != nil {
		return err
	}
	return nil
}

func isThere(ctx context.Context, db *pgx.Conn, userID, randomCode, ip string) (bool, error) {
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM clusterinfo
			WHERE user_id = $1 AND random_code = $2 AND instance_public_ip = $3
		)`

	var exists bool
	err := db.QueryRow(ctx, q, userID, randomCode, ip).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

func main() {
	time.Sleep(time.Minute * 2)
	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	ctx := context.Background()
	defer cancel()
	connStr := os.Getenv("DATABASE_URL")
	conn, err := pgx.Connect(startCtx, connStr)
	if err != nil {
		panic(err)
	}
	defer conn.Close(ctx)
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		log.Fatalln("Failed To Get Namespace ")
	}
	dispatch := os.Getenv("DISPATCH_NAME")
	if dispatch == "" {
		log.Fatalln("Failed To Get Dispatch Name ")
	}
	podName := os.Getenv("POD_NAME")
	if podName == "" {
		log.Fatalln("Failed To Get Pod Name ")
	}
	uuid := os.Getenv("UUID")
	if uuid == "" {
		log.Fatalln("Failed To Get UUID ")
	}
	randomcode := os.Getenv("RANDSTR")
	if randomcode == "" {
		log.Fatalln("Failed To Get Random Code ")
	}
	role := os.Getenv("ROLE")
	if role == "" {
		log.Fatalln("Failed To Get Role ")
	}
	IPTOPING := os.Getenv("IP")
	if IPTOPING == "" {
		log.Fatalln("Failed To Get IP ")
	}
	scheme := runtime.NewScheme()
	err = clientgoscheme.AddToScheme(scheme)
	if err != nil {
		log.Fatalln("Failed To Load Default Schemes ", err)
	}
	err = dispatchv1.AddToScheme(scheme)
	if err != nil {
		log.Fatalln("Failed To Load Custom Operator ", err)
	}

	c, err := kcli.New(ctrl.GetConfigOrDie(), kcli.Options{Scheme: scheme})
	if err != nil {
		log.Fatalln("Failed To Load Default Context ", err)
	}
	k8client := kubernetes.NewForConfigOrDie(ctrl.GetConfigOrDie())
	cli := k8client.CoreV1()
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	exists, err := isThere(ctx, conn, uuid, randomcode, IPTOPING)
	if err != nil {
		log.Fatalln("Failed To Connect To the Database  ", err)
	}
	if exists == false && isAlive(IPTOPING, 2*time.Second) {
		if err := insertRow(ctx, uuid, randomcode, conn, IPTOPING, role); err != nil {
			log.Println("Failed To Make a entry in Database ", err)
		}
		patch, _ := json.Marshal(map[string]any{
			"data": map[string]string{IPTOPING: podName},
		})
		_, err := cli.ConfigMaps(namespace).Patch(
			ctx, "alivenodeips", types.MergePatchType, patch, metav1.PatchOptions{},
		)
		if err != nil {
			log.Println("Failed To make Entry to alivenodeips ConfigMap ", err)
		}
	}
	if role == "control-plane" {
		for range ticker.C {
			if !isAlive(IPTOPING, 2*time.Second) {
				time.Sleep(time.Minute * 5)
				if isAlive(IPTOPING, 2*time.Second) {
					continue
				} else {
					log.Println("Control-Plane is Dead")
					var got dispatchv1.ClusterInit
					key := kcli.ObjectKey{Namespace: namespace, Name: dispatch}
					if err := c.Get(ctx, key, &got); err != nil {
						log.Fatalln("Failed To get The ClusterInit Resource ", err)
					}
					patch := kcli.MergeFromWithOptions(got.DeepCopy(), kcli.MergeFromWithOptimisticLock{})
					if controllerutil.RemoveFinalizer(&got, "dispatch.ec2-launchpad.io/finalizer") {
						if err := c.Patch(ctx, &got, patch); err != nil {
							log.Fatalln("Failed To Patch The Custom Resource ", err)
						}
					}
					if err := deleteEveryRow(ctx, uuid, randomcode, conn); err != nil {
						log.Println("Failed To Delete Rows from the Database ", err)
					}
					if err = cli.Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{}); err != nil {
						log.Fatalln("Failed To Delete Control Node Pod", err)
					}
				}
			}
		}
	} else {
		for range ticker.C {
			if !isAlive(IPTOPING, 2*time.Second) {
				time.Sleep(time.Minute * 5)
				if isAlive(IPTOPING, 2*time.Second) {
					continue
				} else {
					log.Println("Worker-Plane is Dead")
					if err := deleteARow(ctx, uuid, randomcode, conn, IPTOPING); err != nil {
						log.Println("Failed To Delete IP from Database ", err)
					}
					patch, _ := json.Marshal(map[string]any{
						"data": map[string]any{IPTOPING: nil},
					})
					_, err := cli.ConfigMaps(namespace).Patch(
						ctx, "alivenodeips", types.MergePatchType, patch, metav1.PatchOptions{},
					)
					if err != nil {
						log.Println("Failed To Delete Entry From CM ", err)
					}
					if err = cli.Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{}); err != nil {
						log.Fatalln("Failed To Delete Worker Node ", err)
					}
				}
			}
		}
	}
}
