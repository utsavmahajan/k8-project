package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
)

func main() {
	crictl := os.Getenv("CRICTL")
	controlnodeIP := os.Getenv("CONTROL_NODE_IP")
	role := os.Getenv("ROLE")
	
	_, err := git.PlainClone("data/scripts", false, &git.CloneOptions{
		URL:      "https://github.com/utsavmahajan/ansible-scripts.git",
		Progress: os.Stdout,
	})
	if err != nil {
		log.Fatalln("clone failed:", err)
	}
	data, err := os.ReadFile("data/scripts/hosts.ini")
	datastr := string(data)
	if err != nil {
		log.Fatalln("Failed TO Open Hosts.ini file ", err)
	}

	if role == "control-plane" {
		helm := os.Getenv("HELM")
		CNI := os.Getenv("CNI")
		gateway := os.Getenv("GATEWAY")
		addons := os.Getenv("ADDONS")
		addonslist := strings.Split(addons, ",")
		ingress := os.Getenv("INGRESS")
		hostIpstr := fmt.Sprintf("\n[servers]\ncontrol-plane ansible_host=%v\ncontrol-plane ansible_user=ubuntu\n", controlnodeIP)
		datastr = hostIpstr + datastr
		err = os.WriteFile("data/scripts/hosts.ini", []byte(datastr), 0644)
		if err != nil {
			log.Fatalln("Failed To Write To Hosts.ini file ", err)
		}
		header := "ansible-playbook -i data/scripts/hosts.ini "
		scripts := header + "data/scripts/cluster-init.yml\n"
		scripts = scripts + header + "data/scripts/init-control-node.yml\n"
		if helm == "true" {
			scripts = scripts + header + "data/scripts/helm.yml\n"
		}
		if crictl == "true" {
			scripts = scripts + header + "data/scripts/crictl.yml\n"
		}
		scripts = scripts + header + "data/scripts/" + CNI + ".yml\n"
		if addons != "" {
			for i := range addonslist {
				scripts = scripts + header + "data/scripts/" + addonslist[i] + ".yml\n"
			}
		}
		if ingress != "" {
			scripts = scripts + header + "data/scripts/" + ingress + ".yml\n"
		}
		if gateway != "" {
			scripts = scripts + header + "data/scripts/" + gateway + ".yml\n"
		}
		os.WriteFile("data/scripts/script.sh", []byte(scripts), 0644)
	} else {
		workernodeIPstr := os.Getenv("WORKER_NODE_IPS")
		workernodeIPList := strings.Split(workernodeIPstr,",")
		host1Ipstr := fmt.Sprintf("control-plane ansible_host=%v\ncontrol-plane ansible_user=ubuntu\n", controlnodeIP)
		host2Ipstr := ""
		limit := "--limit "
		nodes := ""
		for i := range workernodeIPList {
			host2Ipstr = host2Ipstr + fmt.Sprintf("worker-plane-%d ansible_host=%v\nworker-plane-%d ansible_user=ubuntu\n",i+1,workernodeIPList[i],i+1)
			nodes = nodes + fmt.Sprintf("worker-plane-%d",i+1) + ","
		}
		nodes = nodes[:len(nodes)-1]
		nodes = fmt.Sprintf(`"%v"`,nodes)
		datastr = "\n[servers]\n" + host1Ipstr + host2Ipstr + datastr
		err = os.WriteFile("data/scripts/hosts.ini", []byte(datastr), 0644)
		if err != nil {
			log.Fatalln("Failed To Write To Hosts.ini file ", err)
		}
		header := "ansible-playbook -i data/scripts/hosts.ini "
		scripts := header + "data/scripts/cluster-init.yml" + " " + limit + nodes +"\n"
		scripts = scripts + header + "data/scripts/worker-node.yml\n"
		if crictl == "true" {
			scripts = scripts + header + "data/scripts/crictl.yml" + " " + limit + nodes + "\n"
		}
		os.WriteFile("data/scripts/script.sh", []byte(scripts), 0644)
	}
}
