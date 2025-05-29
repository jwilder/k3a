az login --tenant TME01.onmicrosoft.com --use-device-code

./k3a cluster create --region canadacentral --subscription 110efc33-11a4-46b9-9986-60716283fbe7 --cluster k3s-canadacentral-vapa-kp4

./k3a pool create --subscription 110efc33-11a4-46b9-9986-60716283fbe7 --name k8s-master --role control-plane --instance-count 1 --sku Standard_D96s_v5 --cluster k3s-canadacentral-vapa-kp3
./k3a kubeconfig --subscription 110efc33-11a4-46b9-9986-60716283fbe7 --cluster k3s-canadacentral-vapa-kp3

./k3a pool create --subscription 110efc33-11a4-46b9-9986-60716283fbe7 --role worker --instance-count 100 --sku Standard_D16s_v3 --cluster k3s-canadacentral-vapa-kp3 --name k8s-agent-1

kubectl get nodes -o name | grep "k3s-agent-" | xargs -I {} kubectl label {} node-role.kubernetes.io/worker=worker --overwrite

az group delete --name $RG --yes --no-wait
etcdctl --endpoints http://4.206.93.140:2379 del --prefix / -w json 
etcdctl --endpoints http://4.206.93.140:2379 compact

etcdctl --endpoints http://4.206.93.140:2379 get --prefix / --keys-only
