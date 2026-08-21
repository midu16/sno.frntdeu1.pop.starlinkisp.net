#!/usr/bin/env bash
# Validate that the node-exporter softirqs/zoneinfo/interrupts collectors report
# data that is CONSISTENT across three vantage points, sampled over a 5-minute window:
#   1. host        - raw kernel counters parsed from the node's /proc/{softirqs,interrupts,zoneinfo}
#   2. node-exporter - the collector's own /metrics scrape (127.0.0.1:9101)
#   3. prometheus  - the value stored in prometheus-k8s (in-cluster query API)
#
# Representative series (one per collector, node/cpu 0):
#   softirqs   node_softirqs_functions_total{cpu="0",type="TIMER"}          (counter, monotonic)
#   interrupts node_interrupts_total{cpu="0",type="LOC"}                    (counter, monotonic)
#   zoneinfo   node_zoneinfo_nr_free_pages{node="0",zone="Normal"}         (gauge, fluctuates)
#   zoneinfo   node_zoneinfo_min_pages{node="0",zone="Normal"}             (gauge, constant watermark)
#
# Output: a set of Markdown tables on stdout, ready to paste into the validation doc.
set -uo pipefail
export KUBECONFIG=${KUBECONFIG:-/home/midu/sno.frntdeu1.pop.starlinkisp.net/workdir/auth/kubeconfig}

NS=openshift-monitoring
SAMPLES=${SAMPLES:-6}
INTERVAL=${INTERVAL:-60}

# integer-normalize (handles scientific notation like 5.337703e+06)
norm() { awk 'BEGIN{printf "%d", (ARGV[1]=="" ? -1 : ARGV[1])}' "$1"; }

declare -a T HS SS PS HI SI PI HZF SZF PZF HZM SZM PZM

for n in $(seq 0 $((SAMPLES-1))); do
  ts=$(oc -n $NS exec ds/node-exporter -c node-exporter -- date -u +%H:%M:%S 2>/dev/null)

  # one exec: raw /proc files + live scrape, separated by markers
  dump=$(oc -n $NS exec ds/node-exporter -c node-exporter -- sh -c \
    'echo "==SOFTIRQS=="; cat /host/root/proc/softirqs; \
     echo "==INTERRUPTS=="; cat /host/root/proc/interrupts; \
     echo "==ZONEINFO=="; cat /host/root/proc/zoneinfo; \
     echo "==METRICS=="; curl -s http://127.0.0.1:9101/metrics' 2>/dev/null)

  soft=$(printf '%s' "$dump" | awk '/==SOFTIRQS==/{f=1;next}/==INTERRUPTS==/{f=0}f')
  intr=$(printf '%s' "$dump" | awk '/==INTERRUPTS==/{f=1;next}/==ZONEINFO==/{f=0}f')
  zone=$(printf '%s' "$dump" | awk '/==ZONEINFO==/{f=1;next}/==METRICS==/{f=0}f')
  metr=$(printf '%s' "$dump" | awk '/==METRICS==/{f=1;next}f')

  # host raw values (CPU0 = field $2 in the softirqs/interrupts tables)
  hs=$(printf '%s' "$soft" | awk '$1=="TIMER:"{print $2}')
  hi=$(printf '%s' "$intr" | awk '$1=="LOC:"{print $2}')
  read hzf hzm < <(printf '%s' "$zone" | awk -v N=0 -v Z=Normal '
      /^Node /{n=$2; sub(",","",n); z=$4}
      n==N && z==Z && $1=="nr_free_pages"{f=$2}
      n==N && z==Z && $1=="min"{m=$2}
      END{print f, m}')

  # node-exporter scrape values (value is always the LAST field; interrupts labels contain spaces)
  ss=$(printf '%s' "$metr" | awk '/^node_softirqs_functions_total{cpu="0",type="TIMER"}/{print $NF}')
  si=$(printf '%s' "$metr" | awk '/^node_interrupts_total{cpu="0",/ && /type="LOC"}/{print $NF}')
  szf=$(printf '%s' "$metr" | awk '/^node_zoneinfo_nr_free_pages{node="0",zone="Normal"}/{print $NF}')
  szm=$(printf '%s' "$metr" | awk '/^node_zoneinfo_min_pages{node="0",zone="Normal"}/{print $NF}')

  # prometheus stored values (one exec, four queries)
  pq() { oc -n $NS exec prometheus-k8s-0 -c prometheus -- \
           curl -s --data-urlencode "query=$1" http://localhost:9090/api/v1/query 2>/dev/null \
         | awk -F'"' '/"value"/{for(i=1;i<=NF;i++) if($i ~ /^[0-9.e+-]+$/){v=$i}} END{print (v==""?"NA":v)}'; }
  ps=$(pq  'node_softirqs_functions_total{cpu="0",type="TIMER"}')
  pi=$(pq  'node_interrupts_total{cpu="0",type="LOC"}')
  pzf=$(pq 'node_zoneinfo_nr_free_pages{node="0",zone="Normal"}')
  pzm=$(pq 'node_zoneinfo_min_pages{node="0",zone="Normal"}')

  T[$n]=$ts
  HS[$n]=$(norm "$hs");  SS[$n]=$(norm "$ss");  PS[$n]=$(norm "$ps")
  HI[$n]=$(norm "$hi");  SI[$n]=$(norm "$si");  PI[$n]=$(norm "$pi")
  HZF[$n]=$(norm "$hzf"); SZF[$n]=$(norm "$szf"); PZF[$n]=$(norm "$pzf")
  HZM[$n]=$(norm "$hzm"); SZM[$n]=$(norm "$szm"); PZM[$n]=$(norm "$pzm")

  echo "[sample $((n+1))/$SAMPLES @ $ts] softirqs h=$hs s=$ss p=$ps | intr h=$hi s=$si p=$pi | zfree h=$hzf s=$szf p=$pzf | zmin h=$hzm s=$szm p=$pzm" >&2
  [ "$n" -lt "$((SAMPLES-1))" ] && sleep "$INTERVAL"
done

emit() { # name unit "hostarr" "scrapearr" "promarr"
  local -n H=$2 S=$3 P=$4
  echo "#### $1"
  echo
  echo "| sample | host UTC | host (/proc) | node-exporter (:9101) | prometheus | host−prom |"
  echo "|--------|----------|--------------|-----------------------|------------|-----------|"
  for n in $(seq 0 $((SAMPLES-1))); do
    printf "| %d | %s | %s | %s | %s | %s |\n" \
      "$((n+1))" "${T[$n]}" "${H[$n]}" "${S[$n]}" "${P[$n]}" "$(( ${H[$n]} - ${P[$n]} ))"
  done
  echo
}

echo "### Raw sampled data (window = $((SAMPLES*INTERVAL - INTERVAL))s + exec time, interval ${INTERVAL}s)"
echo
emit "softirqs — node_softirqs_functions_total{cpu=0,type=TIMER} (counter)" HS SS PS
emit "interrupts — node_interrupts_total{cpu=0,type=LOC} (counter)"        HI SI PI
emit "zoneinfo — node_zoneinfo_nr_free_pages{node=0,zone=Normal} (gauge)"  HZF SZF PZF
emit "zoneinfo — node_zoneinfo_min_pages{node=0,zone=Normal} (constant watermark)" HZM SZM PZM
