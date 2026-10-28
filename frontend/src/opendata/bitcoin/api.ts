import axios from 'axios';
import type {
  BtcBaselinePlan,
  BtcBaselineStatus,
  BtcBQEstimateResponse,
  BtcFeesData,
  BtcHoldersData,
  BtcLiveData,
  BtcMarketsData,
  BtcMetaData,
  BtcMiningData,
  BtcMiningPeriod,
  BtcOnchainData,
  BtcOnchainHoldersData,
  BtcOverviewData,
  BtcValuationData,
  BtcValuationRange,
} from './types';

// BigQuery actions are loopback-only on the server and need this custom header
// (plus JSON), so other devices and other websites cannot trigger spend.
export const BTC_BQ_HEADERS = {
  'Content-Type': 'application/json',
  'X-BigLens-Action': 'bitcoin-bq',
} as const;

export type BtcBQPostFn = <T>(
  url: string,
  body: unknown,
  config?: { headers?: Record<string, string> },
) => Promise<{ data: T }>;

const defaultPost: BtcBQPostFn = (url, body, config) => axios.post(url, body, config);

export const fetchBitcoinOverview = async (): Promise<BtcOverviewData> => {
  const { data } = await axios.get<BtcOverviewData>('/api/opendata/bitcoin/overview');
  return data;
};

export const fetchBitcoinMeta = async (): Promise<BtcMetaData> => {
  const { data } = await axios.get<BtcMetaData>('/api/opendata/bitcoin/meta');
  return data;
};

export const fetchBitcoinLive = async (): Promise<BtcLiveData> => {
  const { data } = await axios.get<BtcLiveData>('/api/opendata/bitcoin/live');
  return data;
};

export const fetchBitcoinMarkets = async (): Promise<BtcMarketsData> => {
  const { data } = await axios.get<BtcMarketsData>('/api/opendata/bitcoin/markets');
  return data;
};

export const fetchBitcoinValuation = async (range: BtcValuationRange): Promise<BtcValuationData> => {
  const { data } = await axios.get<BtcValuationData>('/api/opendata/bitcoin/valuation', { params: { range } });
  return data;
};

export const fetchBitcoinMining = async (period: BtcMiningPeriod): Promise<BtcMiningData> => {
  const { data } = await axios.get<BtcMiningData>('/api/opendata/bitcoin/mining', { params: { period } });
  return data;
};

export const fetchBitcoinFees = async (period: '24h' | '1w' | '1m'): Promise<BtcFeesData> => {
  const { data } = await axios.get<BtcFeesData>('/api/opendata/bitcoin/fees', { params: { period } });
  return data;
};

export const fetchBitcoinOnchain = async (days: number): Promise<BtcOnchainData> => {
  const { data } = await axios.get<BtcOnchainData>('/api/opendata/bitcoin/onchain', { params: { days } });
  return data;
};

export const fetchBitcoinHolders = async (days: number): Promise<BtcHoldersData> => {
  const { data } = await axios.get<BtcHoldersData>('/api/opendata/bitcoin/holders', { params: { days } });
  return data;
};

export const fetchBitcoinOnchainHolders = async (days: number): Promise<BtcOnchainHoldersData> => {
  const { data } = await axios.get<BtcOnchainHoldersData>('/api/opendata/bitcoin/onchain/holders', { params: { days } });
  return data;
};

export const fetchBitcoinBaseline = async (): Promise<BtcBaselineStatus> => {
  const { data } = await axios.get<BtcBaselineStatus>('/api/opendata/bitcoin/onchain/baseline');
  return data;
};

export const dryRunBitcoinBaseline = async (): Promise<BtcBaselinePlan> => {
  const { data } = await axios.post<BtcBaselinePlan>('/api/opendata/bitcoin/onchain/baseline', {
    action: 'dry_run',
  });
  return data;
};

export const confirmBitcoinBaseline = async (token: string): Promise<BtcBaselineStatus> => {
  const { data } = await axios.post<BtcBaselineStatus>('/api/opendata/bitcoin/onchain/baseline', {
    action: 'confirm',
    token,
  });
  return data;
};

// estimateBitcoinBQJob runs the free dry-run preview of the next window.
export const estimateBitcoinBQJob = async (
  job: 'btc_onchain' | 'btc_coinage' | string,
  post: BtcBQPostFn = defaultPost,
): Promise<BtcBQEstimateResponse> => {
  const { data } = await post<BtcBQEstimateResponse>(
    `/api/opendata/bitcoin/bigquery/estimate?job=${encodeURIComponent(job)}`,
    {},
    { headers: { ...BTC_BQ_HEADERS } },
  );
  return data;
};

// runBitcoinBQJob starts a manual run of the previewed window; the server
// refuses it when a fresh estimate exceeds confirmedEstimateBytes by > 10%.
export const runBitcoinBQJob = async (
  job: 'btc_onchain' | 'btc_coinage' | string,
  confirmedEstimateBytes: number,
  post: BtcBQPostFn = defaultPost,
): Promise<{ status: string; job: string }> => {
  const { data } = await post<{ status: string; job: string }>(
    `/api/opendata/bitcoin/bigquery/run?job=${encodeURIComponent(job)}`,
    { confirmed_estimate_bytes: confirmedEstimateBytes },
    { headers: { ...BTC_BQ_HEADERS } },
  );
  return data;
};
