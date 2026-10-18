import axios from 'axios';
import type {
  BtcBaselinePlan,
  BtcBaselineStatus,
  BtcFeesData,
  BtcHoldersData,
  BtcLiveData,
  BtcMarketsData,
  BtcMetaData,
  BtcOnchainData,
  BtcOnchainHoldersData,
} from './types';

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
