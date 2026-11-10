120 failing runs (20 per fault type), mean 184 lines each.

#### All faults
| tool | lines to read | fault lines found | root cause line found | precision | a fault in block #1 | in top 3 |
|---|---:|---:|---:|---:|---:|---:|
| grep -iE 'error|fail|fatal|exception|panic' | 20.1 | 57% | 50% | 56% | – | – |
| diff (1 passing run) | 183.9 | 100% | 100% | 13% | – | – |
| regex-normalise + diff (1 passing run) | 81.2 | 100% | 97% | 40% | – | – |
| regex-normalise + set difference (1 passing run) | 20.0 | 98% | 97% | 56% | – | – |
| regex-normalise + set difference (3 passing runs) | 19.1 | 95% | 92% | 56% | – | – |
| whyred (1 passing run) | 18.0 | 97% | 97% | 60% | 80% | 100% |
| whyred (2 passing runs) | 11.6 | 97% | 97% | 93% | 85% | 99% |
| whyred (3 passing runs) | 10.9 | 95% | 92% | 97% | 87% | 100% |
| whyred (5 passing runs) | 10.6 | 94% | 91% | 98% | 89% | 100% |

#### False alarms on a passing run (lines flagged; ideal is 0)
| tool | lines flagged |
|---|---:|
| grep -iE 'error|fail|fatal|exception|panic' | 26.3 |
| diff (1 passing run) | 329.6 |
| regex-normalise + diff (1 passing run) | 141.5 |
| regex-normalise + set difference (1 passing run) | 16.0 |
| regex-normalise + set difference (3 passing runs) | 14.1 |
| whyred (1 passing run) | 10.3 |
| whyred (2 passing runs) | 1.1 |
| whyred (3 passing runs) | 0.5 |
| whyred (5 passing runs) | 0.2 |

#### compile_error
| tool | lines to read | fault lines found | root cause line found | precision | a fault in block #1 | in top 3 |
|---|---:|---:|---:|---:|---:|---:|
| grep -iE 'error|fail|fatal|exception|panic' | 5.2 | 63% | 100% | 97% | – | – |
| diff (1 passing run) | 80.3 | 100% | 100% | 10% | – | – |
| regex-normalise + diff (1 passing run) | 12.6 | 100% | 100% | 64% | – | – |
| regex-normalise + set difference (1 passing run) | 12.6 | 100% | 100% | 64% | – | – |
| regex-normalise + set difference (3 passing runs) | 12.2 | 100% | 100% | 66% | – | – |
| whyred (1 passing run) | 12.1 | 96% | 100% | 63% | 85% | 100% |
| whyred (2 passing runs) | 8.1 | 96% | 100% | 96% | 85% | 95% |
| whyred (3 passing runs) | 7.9 | 96% | 100% | 98% | 85% | 100% |
| whyred (5 passing runs) | 7.8 | 96% | 100% | 98% | 85% | 100% |

#### dep_drift
| tool | lines to read | fault lines found | root cause line found | precision | a fault in block #1 | in top 3 |
|---|---:|---:|---:|---:|---:|---:|
| grep -iE 'error|fail|fatal|exception|panic' | 32.6 | 44% | 0% | 23% | – | – |
| diff (1 passing run) | 306.7 | 100% | 100% | 5% | – | – |
| regex-normalise + diff (1 passing run) | 154.9 | 100% | 100% | 10% | – | – |
| regex-normalise + set difference (1 passing run) | 29.7 | 100% | 100% | 54% | – | – |
| regex-normalise + set difference (3 passing runs) | 29.2 | 100% | 100% | 55% | – | – |
| whyred (1 passing run) | 26.4 | 100% | 100% | 62% | 70% | 100% |
| whyred (2 passing runs) | 16.4 | 100% | 100% | 98% | 85% | 100% |
| whyred (3 passing runs) | 16.2 | 100% | 100% | 99% | 85% | 100% |
| whyred (5 passing runs) | 16.1 | 100% | 100% | 99% | 90% | 100% |

#### hang
| tool | lines to read | fault lines found | root cause line found | precision | a fault in block #1 | in top 3 |
|---|---:|---:|---:|---:|---:|---:|
| grep -iE 'error|fail|fatal|exception|panic' | 28.2 | 33% | 0% | 4% | – | – |
| diff (1 passing run) | 285.2 | 100% | 100% | 1% | – | – |
| regex-normalise + diff (1 passing run) | 132.4 | 100% | 100% | 2% | – | – |
| regex-normalise + set difference (1 passing run) | 16.9 | 100% | 100% | 18% | – | – |
| regex-normalise + set difference (3 passing runs) | 16.2 | 100% | 100% | 19% | – | – |
| whyred (1 passing run) | 11.8 | 100% | 100% | 27% | 55% | 100% |
| whyred (2 passing runs) | 4.0 | 100% | 100% | 78% | 65% | 100% |
| whyred (3 passing runs) | 3.3 | 100% | 100% | 92% | 70% | 100% |
| whyred (5 passing runs) | 3.2 | 100% | 100% | 94% | 75% | 100% |

#### network
| tool | lines to read | fault lines found | root cause line found | precision | a fault in block #1 | in top 3 |
|---|---:|---:|---:|---:|---:|---:|
| grep -iE 'error|fail|fatal|exception|panic' | 18.9 | 100% | 100% | 100% | – | – |
| diff (1 passing run) | 44.0 | 100% | 100% | 43% | – | – |
| regex-normalise + diff (1 passing run) | 22.9 | 99% | 80% | 82% | – | – |
| regex-normalise + set difference (1 passing run) | 20.5 | 87% | 80% | 78% | – | – |
| regex-normalise + set difference (3 passing runs) | 17.4 | 72% | 55% | 76% | – | – |
| whyred (1 passing run) | 20.7 | 88% | 80% | 79% | 100% | 100% |
| whyred (2 passing runs) | 16.9 | 88% | 80% | 98% | 100% | 100% |
| whyred (3 passing runs) | 14.3 | 75% | 55% | 100% | 100% | 100% |
| whyred (5 passing runs) | 12.9 | 69% | 45% | 100% | 100% | 100% |

#### oom
| tool | lines to read | fault lines found | root cause line found | precision | a fault in block #1 | in top 3 |
|---|---:|---:|---:|---:|---:|---:|
| grep -iE 'error|fail|fatal|exception|panic' | 8.2 | 62% | 0% | 98% | – | – |
| diff (1 passing run) | 87.2 | 100% | 100% | 15% | – | – |
| regex-normalise + diff (1 passing run) | 17.4 | 100% | 100% | 75% | – | – |
| regex-normalise + set difference (1 passing run) | 17.4 | 100% | 100% | 75% | – | – |
| regex-normalise + set difference (3 passing runs) | 16.9 | 100% | 100% | 77% | – | – |
| whyred (1 passing run) | 17.4 | 100% | 100% | 75% | 90% | 100% |
| whyred (2 passing runs) | 13.6 | 100% | 100% | 96% | 90% | 100% |
| whyred (3 passing runs) | 13.4 | 100% | 100% | 97% | 90% | 100% |
| whyred (5 passing runs) | 13.2 | 100% | 100% | 99% | 95% | 100% |

#### test_fail
| tool | lines to read | fault lines found | root cause line found | precision | a fault in block #1 | in top 3 |
|---|---:|---:|---:|---:|---:|---:|
| grep -iE 'error|fail|fatal|exception|panic' | 27.4 | 41% | 100% | 15% | – | – |
| diff (1 passing run) | 300.2 | 100% | 100% | 3% | – | – |
| regex-normalise + diff (1 passing run) | 147.1 | 100% | 100% | 7% | – | – |
| regex-normalise + set difference (1 passing run) | 22.7 | 100% | 100% | 44% | – | – |
| regex-normalise + set difference (3 passing runs) | 22.2 | 100% | 100% | 45% | – | – |
| whyred (1 passing run) | 19.6 | 100% | 100% | 52% | 80% | 100% |
| whyred (2 passing runs) | 10.5 | 100% | 100% | 96% | 85% | 100% |
| whyred (3 passing runs) | 10.2 | 100% | 100% | 99% | 90% | 100% |
| whyred (5 passing runs) | 10.1 | 100% | 100% | 99% | 90% | 100% |

