import axios from 'axios';
import type { StableAsset, StableRange } from './deeplink';
import type {
  StableChains,
  StableControls,
  StableLive,
  StableMeta,
  StableOverview,
  StablePeg,
  StableReserves,
  StableSupply,
} from './types';

// Every endpoint is served from BigLens' own samples and SQLite; none calls an
// upstream per request (stablecoin.md §9).
const BASE = '/api/opendata/stablecoin';

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
