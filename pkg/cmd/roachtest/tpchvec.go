// Copyright 2019 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/cockroachdb/cockroach/pkg/util/randutil"
	"github.com/cockroachdb/errors"
)

func registerTPCHVec(r *testRegistry) {
	const (
		nodeCount      = 3
		numTPCHQueries = 22
	)

	type crdbVersion int
	const (
		version19_2 crdbVersion = iota
		version20_1
	)
	toCRDBVersion := func(v string) (crdbVersion, error) {
		if strings.HasPrefix(v, "v19.2") {
			return version19_2, nil
		} else if strings.HasPrefix(v, "v20.1") {
			return version20_1, nil
		} else {
			return 0, errors.Errorf("unrecognized version: %s", v)
		}
	}

	// queriesToSkipByVersion is a map from crdbVersion to another map that
	// contains query numbers to be skipped (as well as the reasons for why
	// they are skipped).
	queriesToSkipByVersion := make(map[crdbVersion]map[int]string)
	queriesToSkipByVersion[version19_2] = map[int]string{
		5:  "can cause OOM",
		7:  "can cause OOM",
		8:  "can cause OOM",
		9:  "can cause OOM",
		19: "can cause OOM",
	}
	vectorizeOnOptionByVersion := map[crdbVersion]string{
		version19_2: "experimental_on",
		version20_1: "on",
	}
	// slownessThreshold describes the threshold at which we fail the test
	// if vec ON is slower that vec OFF, meaning that if
	// vec_on_time > vecOnSlowerFailFactor * vec_off_time, the test is failed.
	// This will help catch any regressions.
	// Note that for 19.2 version the threshold is higher in order to reduce
	// the noise.
	slownessThresholdByVersion := map[crdbVersion]float64{
		version19_2: 1.5,
		version20_1: 1.2,
	}

	TPCHTables := []string{
		"nation", "region", "part", "supplier",
		"partsupp", "customer", "orders", "lineitem",
	}
	TPCHTableStatsInjection := []string{
		`ALTER TABLE region INJECT STATISTICS '[
				{
					"columns": ["r_regionkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 5,
					"distinct_count": 5
				},
				{
					"columns": ["r_name"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 5,
					"distinct_count": 5
				},
				{
					"columns": ["r_comment"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 5,
					"distinct_count": 5
				}
			]';`,
		`ALTER TABLE nation INJECT STATISTICS '[
				{
					"columns": ["n_nationkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 25,
					"distinct_count": 25
				},
				{
					"columns": ["n_name"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 25,
					"distinct_count": 25
				},
				{
					"columns": ["n_regionkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 25,
					"distinct_count": 5
				},
				{
					"columns": ["n_comment"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 25,
					"distinct_count": 25
				}
			]';`,
		`ALTER TABLE supplier INJECT STATISTICS '[
				{
					"columns": ["s_suppkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 10000,
					"distinct_count": 10000
				},
				{
					"columns": ["s_name"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 10000,
					"distinct_count": 10000
				},
				{
					"columns": ["s_address"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 10000,
					"distinct_count": 10000
				},
				{
					"columns": ["s_nationkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 10000,
					"distinct_count": 25
				},
				{
					"columns": ["s_phone"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 10000,
					"distinct_count": 10000
				},
				{
					"columns": ["s_acctbal"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 10000,
					"distinct_count": 10000
				},
				{
					"columns": ["s_comment"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 10000,
					"distinct_count": 10000
				}
			]';`,
		`ALTER TABLE public.part INJECT STATISTICS '[
				{
					"columns": ["p_partkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 200000,
					"distinct_count": 200000
				},
				{
					"columns": ["p_name"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 200000,
					"distinct_count": 200000
				},
				{
					"columns": ["p_mfgr"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 200000,
					"distinct_count": 5
				},
				{
					"columns": ["p_brand"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 200000,
					"distinct_count": 25
				},
				{
					"columns": ["p_type"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 200000,
					"distinct_count": 150
				},
				{
					"columns": ["p_size"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 200000,
					"distinct_count": 50
				},
				{
					"columns": ["p_container"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 200000,
					"distinct_count": 40
				},
				{
					"columns": ["p_retailprice"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 200000,
					"distinct_count": 20000
				},
				{
					"columns": ["p_comment"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 200000,
					"distinct_count": 130000
				}
			]';`,
		`ALTER TABLE partsupp INJECT STATISTICS '[
				{
					"columns": ["ps_partkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 800000,
					"distinct_count": 200000
				},
				{
					"columns": ["ps_suppkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 800000,
					"distinct_count": 10000
				},
				{
					"columns": ["ps_availqty"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 800000,
					"distinct_count": 10000
				},
				{
					"columns": ["ps_supplycost"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 800000,
					"distinct_count": 100000
				},
				{
					"columns": ["ps_comment"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 800000,
					"distinct_count": 800000
				}
			]';`,
		`ALTER TABLE customer INJECT STATISTICS '[
				{
					"columns": ["c_custkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 150000,
					"distinct_count": 150000
				},
				{
					"columns": ["c_name"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 150000,
					"distinct_count": 150000
				},
				{
					"columns": ["c_address"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 150000,
					"distinct_count": 150000
				},
				{
					"columns": ["c_nationkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 150000,
					"distinct_count": 25
				},
				{
					"columns": ["c_phone"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 150000,
					"distinct_count": 150000
				},
				{
					"columns": ["c_acctbal"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 150000,
					"distinct_count": 150000
				},
				{
					"columns": ["c_mktsegment"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 150000,
					"distinct_count": 5
				},
				{
					"columns": ["c_comment"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 150000,
					"distinct_count": 150000
				}
			]';`,
		`ALTER TABLE orders INJECT STATISTICS '[
				{
					"columns": ["o_orderkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 1500000,
					"distinct_count": 1500000
				},
				{
					"columns": ["o_custkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 1500000,
					"distinct_count": 100000
				},
				{
					"columns": ["o_orderstatus"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 1500000,
					"distinct_count": 3
				},
				{
					"columns": ["o_totalprice"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 1500000,
					"distinct_count": 1500000
				},
				{
					"columns": ["o_orderdate"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 1500000,
					"distinct_count": 2500
				},
				{
					"columns": ["o_orderpriority"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 1500000,
					"distinct_count": 5
				},
				{
					"columns": ["o_clerk"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 1500000,
					"distinct_count": 1000
				},
				{
					"columns": ["o_shippriority"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 1500000,
					"distinct_count": 1
				},
				{
					"columns": ["o_comment"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 1500000,
					"distinct_count": 1500000
				}
			]';`,
		`ALTER TABLE lineitem INJECT STATISTICS '[
				{
					"columns": ["l_orderkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 1500000
				},
				{
					"columns": ["l_partkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 200000
				},
				{
					"columns": ["l_suppkey"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 10000
				},
				{
					"columns": ["l_linenumber"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 7
				},
				{
					"columns": ["l_quantity"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 50
				},
				{
					"columns": ["l_extendedprice"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 1000000
				},
				{
					"columns": ["l_discount"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 11
				},
				{
					"columns": ["l_tax"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 9
				},
				{
					"columns": ["l_returnflag"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 3
				},
				{
					"columns": ["l_linestatus"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 2
				},
				{
					"columns": ["l_shipdate"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 2500
				},
				{
					"columns": ["l_commitdate"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 2500
				},
				{
					"columns": ["l_receiptdate"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 2500
				},
				{
					"columns": ["l_shipinstruct"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 4
				},
				{
					"columns": ["l_shipmode"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 7
				},
				{
					"columns": ["l_comment"],
					"created_at": "2018-01-01 1:00:00.00000+00:00",
					"row_count": 6001215,
					"distinct_count": 4500000
				}
			]';`,
	}

	type runOption int
	const (
		// perf configuration is meant to be used to check the correctness of
		// the vectorized engine and compare the queries' runtimes against
		// row-by-row engine.
		perf runOption = iota
		// stressDiskSpilling configuration is meant to stress disk spilling of
		// the vectorized engine. There is no comparison of the runtimes.
		stressDiskSpilling
	)
	type runConfig struct {
		vectorizeOptions   []bool
		stressDiskSpilling bool
		numRunsPerQuery    int
	}
	runConfigs := make(map[runOption]runConfig)
	const (
		// These correspond to "perf" run configuration below.
		vecOnConfig  = 0
		vecOffConfig = 1
	)
	runConfigs[perf] = runConfig{
		vectorizeOptions:   []bool{true, false},
		stressDiskSpilling: false,
		numRunsPerQuery:    3,
	}
	runConfigs[stressDiskSpilling] = runConfig{
		vectorizeOptions:   []bool{true},
		stressDiskSpilling: true,
		numRunsPerQuery:    1,
	}

	runTPCHVec := func(ctx context.Context, t *test, c *cluster, option runOption) {
		firstNode := c.Node(1)
		c.Put(ctx, cockroach, "./cockroach", c.All())
		c.Put(ctx, workload, "./workload", firstNode)
		c.Start(ctx, t)

		conn := c.Conn(ctx, 1)
		t.Status("restoring TPCH dataset for Scale Factor 1")
		setup := `
CREATE DATABASE tpch;
RESTORE tpch.* FROM 'gs://cockroach-fixtures/workload/tpch/scalefactor=1/backup' WITH into_db = 'tpch';
`
		if _, err := conn.Exec(setup); err != nil {
			t.Fatal(err)
		}

		t.Status("scattering the data")
		if _, err := conn.Exec("USE tpch;"); err != nil {
			t.Fatal(err)
		}
		for _, table := range TPCHTables {
			scatter := fmt.Sprintf("ALTER TABLE %s SCATTER;", table)
			if _, err := conn.Exec(scatter); err != nil {
				t.Fatal(err)
			}
		}
		t.Status("waiting for full replication")
		waitForFullReplication(t, conn)
		t.Status("injecting stats")
		for _, injectStats := range TPCHTableStatsInjection {
			if _, err := conn.Exec(injectStats); err != nil {
				t.Fatal(err)
			}
		}
		versionString, err := fetchCockroachVersion(ctx, c, c.Node(1)[0])
		if err != nil {
			t.Fatal(err)
		}
		version, err := toCRDBVersion(versionString)
		if err != nil {
			t.Fatal(err)
		}
		queriesToSkip := queriesToSkipByVersion[version]
		runConfig := runConfigs[option]
		rng, _ := randutil.NewPseudoRand()
		if runConfig.stressDiskSpilling {
			// In order to stress the disk spilling of the vectorized
			// engine, we will set workmem limit to a random value in range
			// [16KiB, 256KiB).
			workmemInKiB := 16 + rng.Intn(240)
			workmem := fmt.Sprintf("%dKiB", workmemInKiB)
			t.Status(fmt.Sprintf("setting workmem='%s'", workmem))
			if _, err := conn.Exec(fmt.Sprintf("SET CLUSTER SETTING sql.distsql.temp_storage.workmem='%s'", workmem)); err != nil {
				t.Fatal(err)
			}
		} else {
			// We are interested in the performance comparison between
			// vectorized and row-by-row engines, so we will reset workmem
			// limit to the default value.
			t.Status("resetting workmem to default")
			if _, err := conn.Exec("RESET CLUSTER SETTING sql.distsql.temp_storage.workmem"); err != nil {
				t.Fatal(err)
			}
		}
		timeByQueryNum := []map[int][]float64{make(map[int][]float64), make(map[int][]float64)}
		for queryNum := 1; queryNum <= numTPCHQueries; queryNum++ {
			for configIdx, vectorize := range runConfig.vectorizeOptions {
				if reason, skip := queriesToSkip[queryNum]; skip {
					t.Status(fmt.Sprintf("skipping q%d because of %q", queryNum, reason))
					continue
				}
				vectorizeSetting := "off"
				if vectorize {
					vectorizeSetting = vectorizeOnOptionByVersion[version]
				}
				cmd := fmt.Sprintf("./workload run tpch --concurrency=1 --db=tpch "+
					"--max-ops=%d --queries=%d --vectorize=%s {pgurl:1-%d}",
					runConfig.numRunsPerQuery, queryNum, vectorizeSetting, nodeCount)
				workloadOutput, err := c.RunWithBuffer(ctx, t.l, firstNode, cmd)
				t.l.Printf("\n" + string(workloadOutput))
				if err != nil {
					// Note: if you see an error like "exit status 1", it is likely caused
					// by the erroneous output of the query.
					t.Fatal(err)
				}
				parseOutput := func(output []byte, timeByQueryNum map[int][]float64) {
					runtimeRegex := regexp.MustCompile(`.*\[q([\d]+)\] returned \d+ rows after ([\d]+\.[\d]+) seconds.*`)
					scanner := bufio.NewScanner(bytes.NewReader(output))
					for scanner.Scan() {
						line := scanner.Bytes()
						match := runtimeRegex.FindSubmatch(line)
						if match != nil {
							queryNum, err := strconv.Atoi(string(match[1]))
							if err != nil {
								t.Fatalf("failed parsing %q as int with %s", match[1], err)
							}
							queryTime, err := strconv.ParseFloat(string(match[2]), 64)
							if err != nil {
								t.Fatalf("failed parsing %q as float with %s", match[2], err)
							}
							timeByQueryNum[queryNum] = append(timeByQueryNum[queryNum], queryTime)
						}
					}
				}
				if option == perf {
					// We only need to parse the output with 'perf' run option.
					parseOutput(workloadOutput, timeByQueryNum[configIdx])
				}
			}
		}
		if option == perf {
			// We are only interested in comparison with 'perf' run option.
			t.Status("comparing the runtimes (only median values for each query are compared)")
			for queryNum := 1; queryNum <= numTPCHQueries; queryNum++ {
				if _, skipped := queriesToSkip[queryNum]; skipped {
					continue
				}
				findMedian := func(times []float64) float64 {
					sort.Float64s(times)
					return times[len(times)/2]
				}
				vecOnTimes := timeByQueryNum[vecOnConfig][queryNum]
				vecOffTimes := timeByQueryNum[vecOffConfig][queryNum]
				if len(vecOnTimes) != runConfig.numRunsPerQuery {
					t.Fatal(fmt.Sprintf("[q%d] unexpectedly wrong number of run times "+
						"recorded with vec ON config: %v", queryNum, vecOnTimes))
				}
				if len(vecOffTimes) != runConfig.numRunsPerQuery {
					t.Fatal(fmt.Sprintf("[q%d] unexpectedly wrong number of run times "+
						"recorded with vec OFF config: %v", queryNum, vecOffTimes))
				}
				vecOnTime := findMedian(vecOnTimes)
				vecOffTime := findMedian(vecOffTimes)
				if vecOffTime < vecOnTime {
					t.l.Printf(
						fmt.Sprintf("[q%d] vec OFF was faster by %.2f%%: "+
							"%.2fs ON vs %.2fs OFF --- WARNING\n"+
							"vec ON times: %v\t vec OFF times: %v",
							queryNum, 100*(vecOnTime-vecOffTime)/vecOffTime,
							vecOnTime, vecOffTime, vecOnTimes, vecOffTimes))
				} else {
					t.l.Printf(
						fmt.Sprintf("[q%d] vec ON was faster by %.2f%%: "+
							"%.2fs ON vs %.2fs OFF\n"+
							"vec ON times: %v\t vec OFF times: %v",
							queryNum, 100*(vecOffTime-vecOnTime)/vecOnTime,
							vecOnTime, vecOffTime, vecOnTimes, vecOffTimes))
				}
				if vecOnTime >= slownessThresholdByVersion[version]*vecOffTime {
					t.Fatal(fmt.Sprintf(
						"[q%d] vec ON is slower by %.2f%% than vec OFF\n"+
							"vec ON times: %v\nvec OFF times: %v",
						queryNum, 100*(vecOnTime-vecOffTime)/vecOffTime, vecOnTimes, vecOffTimes))
				}
			}
		}
	}

	r.Add(testSpec{
		Name:       "tpchvec/perf",
		Owner:      OwnerSQLExec,
		Cluster:    makeClusterSpec(nodeCount),
		MinVersion: "v19.2.0",
		Run: func(ctx context.Context, t *test, c *cluster) {
			runTPCHVec(ctx, t, c, perf)
		},
	})
	r.Add(testSpec{
		Name:    "tpchvec/disk",
		Owner:   OwnerSQLExec,
		Cluster: makeClusterSpec(nodeCount),
		// 19.2 version doesn't have disk spilling nor memory monitoring, so
		// there is no point in running this config on that version.
		MinVersion: "v20.1.0",
		Run: func(ctx context.Context, t *test, c *cluster) {
			runTPCHVec(ctx, t, c, stressDiskSpilling)
		},
	})
}
