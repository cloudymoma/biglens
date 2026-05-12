import axios from 'axios';
import type {
  QueryFilters,
  StorageDashboardData,
  ComputeDashboardData,
  CostDashboardData,
  InsightsDashboardData,
  IAMDashboardData,
} from './types';

function filterParams(filters: QueryFilters): Record<string, string> {
  const p: Record<string, string> = {};
  if (filters.region) p.region = filters.region;
  if (filters.dataset) p.dataset = filters.dataset;
  if (filters.table) p.table = filters.table;
  if (filters.user_email) p.user_email = filters.user_email;
  if (filters.time_range) p.time_range = filters.time_range;
  return p;
}

export async function fetchStorageDashboard(filters: QueryFilters): Promise<StorageDashboardData> {
  const { data } = await axios.get('/api/dashboard/storage', { params: filterParams(filters) });
  return data;
}

export async function fetchComputeDashboard(filters: QueryFilters): Promise<ComputeDashboardData> {
  const { data } = await axios.get('/api/dashboard/compute', { params: filterParams(filters) });
  return data;
}

export async function fetchCostDashboard(filters: QueryFilters): Promise<CostDashboardData> {
  const { data } = await axios.get('/api/dashboard/cost', { params: filterParams(filters) });
  return data;
}

export async function fetchInsightsDashboard(filters: QueryFilters): Promise<InsightsDashboardData> {
  const { data } = await axios.get('/api/dashboard/insights', { params: filterParams(filters) });
  return data;
}

export async function fetchIAMDashboard(region: string, emails: string[], timeRange: string): Promise<IAMDashboardData> {
  const params: Record<string, string> = { region, time_range: timeRange };
  if (emails.length > 0) params.emails = emails.join(',');
  const { data } = await axios.get('/api/dashboard/iam', { params });
  return data;
}

export async function fetchEmailSuggestions(region: string, query: string): Promise<string[]> {
  try {
    const { data } = await axios.get('/api/iam/emails', { params: { region, q: query } });
    return data || [];
  } catch {
    return [];
  }
}

export async function fetchRegions(): Promise<string[]> {
  try {
    const { data } = await axios.get('/api/regions');
    return data || [];
  } catch {
    return ['us', 'eu'];
  }
}

export async function fetchConfig(): Promise<any> {
  const { data } = await axios.get('/api/config');
  return data;
}

export async function fetchDatasets(): Promise<string[]> {
  try {
    const { data } = await axios.get('/api/datasets');
    return data || [];
  } catch {
    return [];
  }
}

export async function fetchTables(datasetId: string): Promise<string[]> {
  try {
    const { data } = await axios.get(`/api/tables?datasetId=${datasetId}`);
    return data || [];
  } catch {
    return [];
  }
}
