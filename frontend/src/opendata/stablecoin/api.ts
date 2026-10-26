import axios from 'axios';
import type { StableAsset, StableRange } from './deeplink';
import type {
  StableBQEstimateResponse,
  StableChains,
  StableControls,
  StableLive,
  StableMeta,
  StableOnchainResponse,
  StableOverview,
  StablePeg,
  StableReserves,
  StableSupply,
} from './types';

// Every GET endpoint is served from BigLens' own samples and SQLite; none calls
// an upstream per request (stablecoin.md §9).
const BASE = '/api/opendata/stablecoin';

export const STABLE_BQ_HEADERS = {
  'Content-Type': 'application/json',
  'X-BigLens-Action': 'stablecoin-bq',
} as const;

export type StableBQPostFn = <T>(
  url: string,
  body: unknown,
  config?: { headers?: Record<string, string> },
) => Promise<{ data: T }>;

const defaultPost: StableBQPostFn = (url, body, config) => axios.post(url, body, config);

export const fetchStableMeta = async (): Promise<StableMeta> => {
  const { data } = await axios.get<StableMeta>(`${BASE}/meta`);
  return data;
};

export const fetchStableLive = async (): Promise<StableLive> => {
  const { data } = await axios.get<StableLive>(`${BASE}/live`);
  return data;
};

export const fetchStableOverview = async (range: StableRange): Promise<StableOverview> => {
  const { data } = await axios.get<StableOverview>(`${BASE}/overview`, { params: { range } });
  return data;
};

export const fetchStablePeg = async (asset: StableAsset, range: StableRange): Promise<StablePeg> => {
  const { data } = await axios.get<StablePeg>(`${BASE}/peg`, { params: { asset, range } });
  return data;
};

export const fetchStableSupply = async (asset: StableAsset, range: StableRange): Promise<StableSupply> => {
  const { data } = await axios.get<StableSupply>(`${BASE}/supply`, { params: { asset, range } });
  return data;
};

export const fetchStableReserves = async (asset: StableAsset): Promise<StableReserves> => {
  const { data } = await axios.get<StableReserves>(`${BASE}/reserves`, { params: { asset } });
  return data;
};

export const fetchStableChains = async (asset: StableAsset): Promise<StableChains> => {
  const { data } = await axios.get<StableChains>(`${BASE}/chains`, { params: { asset } });
  return data;
};

export const fetchStableControls = async (asset: StableAsset): Promise<StableControls> => {
  const { data } = await axios.get<StableControls>(`${BASE}/controls`, { params: { asset } });
  return data;
};

export const fetchStableOnchain = async (asset: StableAsset, days = 90): Promise<StableOnchainResponse> => {
  const { data } = await axios.get<StableOnchainResponse>(`${BASE}/onchain`, { params: { asset, days } });
  return data;
};

export const estimateStableBQJob = async (
  job: 'stable_eth' | 'stable_tron' | string,
  post: StableBQPostFn = defaultPost,
): Promise<StableBQEstimateResponse> => {
  const { data } = await post<StableBQEstimateResponse>(
    `${BASE}/bigquery/estimate?job=${encodeURIComponent(job)}`,
    {},
    { headers: { ...STABLE_BQ_HEADERS } },
  );
  return data;
};

export const runStableBQJob = async (
  job: 'stable_eth' | 'stable_tron' | string,
  confirmedEstimateBytes: number,
  post: StableBQPostFn = defaultPost,
): Promise<{ status: string; job: string }> => {
  const { data } = await post<{ status: string; job: string }>(
    `${BASE}/bigquery/run?job=${encodeURIComponent(job)}`,
    { confirmed_estimate_bytes: confirmedEstimateBytes },
    { headers: { ...STABLE_BQ_HEADERS } },
  );
  return data;
};

