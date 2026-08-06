import axios from 'axios';
import type {
  BillingFilterState,
  BillingConfigResponse,
  BillingMeta,
  BillingOverviewData,
  BillingServicesData,
  BillingProjectsData,
  BillingResourcesData,
  BillingCreditsData,
  BillingPricingData,
} from './types';
import type {
  QueryFilters,
  StorageDashboardData,
  ComputeDashboardData,
  CostDashboardData,
  InsightsDashboardData,
  JobsDashboardData,
  IAMDashboardData,
  SecurityDashboardData,
  CatalogGraph,
  GraphNode,
  Concept,
  ConceptDetail,
  CatalogTypeCount,
  CatalogManifest,
  ImportResult,
  TrendsMeta,
  TrendsDashboardData,
  TrendsTermData,
  GdeltEventsData,
  GdeltGkgData,
  GdeltDyadsData,
  GdeltCountryData,
  GdeltImpactData,
  GdeltStoriesData,
  GdeltIndustryData,
  GdeltIndustryKey,
  WeatherMeta,
  WeatherDashboardData,
  CryptoPulseData,
  CryptoFeesData,
  CryptoWhalesData,
  CryptoTokensData,
  CryptoMiningData,
  CryptoSpotData,
  CryptoChain,
} from './types';

function filterParams(filters: QueryFilters): Record<string, string> {
  const p: Record<string, string> = {};
  if (filters.region) p.region = filters.region;
  if (filters.dataset) p.dataset = filters.dataset;
  if (filters.table) p.table = filters.table;
  if (filters.user_email) p.user_email = filters.user_email;
  if (filters.time_range) p.time_range = filters.time_range;
  if (filters.job_type) p.job_type = filters.job_type;
  if (filters.status) p.status = filters.status;
  if (filters.cache_hit) p.cache_hit = filters.cache_hit;
  if (filters.billing) p.billing = filters.billing;
  if (filters.principal) p.principal = filters.principal;
  if (filters.group_by && filters.group_by !== 'user') p.group_by = filters.group_by;
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

export async function fetchJobsDashboard(filters: QueryFilters): Promise<JobsDashboardData> {
  const { data } = await axios.get('/api/dashboard/jobs', { params: filterParams(filters) });
  return data;
}

export async function fetchIAMDashboard(region: string, emails: string[], timeRange: string): Promise<IAMDashboardData> {
  const params: Record<string, string> = { region, time_range: timeRange };
  if (emails.length > 0) params.emails = emails.join(',');
  const { data } = await axios.get('/api/dashboard/iam', { params });
  return data;
}

export async function fetchSecurityDashboard(region: string, timeRange: string): Promise<SecurityDashboardData> {
  const { data } = await axios.get('/api/dashboard/security', { params: { region, time_range: timeRange } });
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

// --- Dataplex / Knowledge Catalog (OKF) ---

export async function fetchCatalogGraph(): Promise<CatalogGraph> {
  const { data } = await axios.get('/api/catalog/graph');
  return { nodes: data.nodes || [], edges: data.edges || [] };
}

export async function searchCatalog(q: string, type: string, tag = ''): Promise<GraphNode[]> {
  const { data } = await axios.get('/api/catalog/search', { params: { q, type, tag } });
  return data || [];
}

export async function fetchConcept(id: string): Promise<ConceptDetail> {
  const { data } = await axios.get('/api/catalog/concept', { params: { id } });
  return data;
}

export async function saveConcept(concept: Concept): Promise<ConceptDetail> {
  const { data } = await axios.put('/api/catalog/concept', concept);
  return data;
}

export async function deleteConcept(id: string): Promise<void> {
  await axios.delete('/api/catalog/concept', { params: { id } });
}

export async function importCatalog(q: string): Promise<ImportResult> {
  const { data } = await axios.post('/api/catalog/import', null, { params: { q } });
  return data;
}

// Re-run the import recorded in the bundle manifest.
export async function refreshCatalogImport(): Promise<ImportResult> {
  const { data } = await axios.post('/api/catalog/import', null, { params: { refresh: '1' } });
  return data;
}

// null = no import recorded yet (404) or manifest unavailable.
export async function fetchCatalogManifest(): Promise<CatalogManifest | null> {
  try {
    const { data } = await axios.get('/api/catalog/manifest');
    return data;
  } catch {
    return null;
  }
}

export async function fetchCatalogTypes(): Promise<CatalogTypeCount[]> {
  try {
    const { data } = await axios.get('/api/catalog/types');
    return data || [];
  } catch {
    return [];
  }
}

// --- BigQuery Open Data: Google Trends ---

export async function fetchTrendsMeta(): Promise<TrendsMeta> {
  const { data } = await axios.get('/api/opendata/trends/meta');
  return data;
}

export async function fetchTrendsDashboard(refreshDate: string, countryCode: string): Promise<TrendsDashboardData> {
  const { data } = await axios.get('/api/opendata/trends/dashboard', {
    params: { refresh_date: refreshDate, country_code: countryCode },
  });
  return data;
}

export async function fetchTrendsTerm(
  refreshDate: string,
  countryCode: string,
  term: string,
  terms: string[],
): Promise<TrendsTermData> {
  const params: Record<string, string> = { refresh_date: refreshDate };
  if (term) params.term = term;
  if (terms.length > 0) {
    params.terms = terms.join(',');
    params.country_code = countryCode;
  }
  const { data } = await axios.get('/api/opendata/trends/term', { params });
  return data;
}

// --- BigQuery Open Data: GDELT ---

export async function fetchGdeltEvents(startDate: string, endDate: string): Promise<GdeltEventsData> {
  const { data } = await axios.get('/api/opendata/gdelt/events', {
    params: { start_date: startDate, end_date: endDate },
  });
  return data;
}

export async function fetchGdeltGkg(startDate: string, endDate: string): Promise<GdeltGkgData> {
  const { data } = await axios.get('/api/opendata/gdelt/gkg', {
    params: { start_date: startDate, end_date: endDate },
  });
  return data;
}

export async function fetchGdeltDyads(startDate: string, endDate: string): Promise<GdeltDyadsData> {
  const { data } = await axios.get('/api/opendata/gdelt/dyads', {
    params: { start_date: startDate, end_date: endDate },
  });
  return data;
}

export async function fetchGdeltCountry(
  startDate: string,
  endDate: string,
  country: string,
): Promise<GdeltCountryData> {
  const { data } = await axios.get('/api/opendata/gdelt/country', {
    params: { start_date: startDate, end_date: endDate, country },
  });
  return data;
}

export async function fetchGdeltImpact(startDate: string, endDate: string): Promise<GdeltImpactData> {
  const { data } = await axios.get('/api/opendata/gdelt/impact', {
    params: { start_date: startDate, end_date: endDate },
  });
  return data;
}

export async function fetchGdeltStories(startDate: string, endDate: string): Promise<GdeltStoriesData> {
  const { data } = await axios.get('/api/opendata/gdelt/stories', {
    params: { start_date: startDate, end_date: endDate },
  });
  return data;
}

export async function fetchGdeltIndustry(
  startDate: string,
  endDate: string,
  industry: GdeltIndustryKey,
): Promise<GdeltIndustryData> {
  const { data } = await axios.get('/api/opendata/gdelt/industry', {
    params: { start_date: startDate, end_date: endDate, industry },
  });
  return data;
}

// --- BigQuery Open Data: NOAA GHCN-Daily weather ---

export async function fetchWeatherMeta(): Promise<WeatherMeta> {
  const { data } = await axios.get('/api/opendata/weather/meta');
  return data;
}

export async function fetchWeatherDashboard(date: string, days: number): Promise<WeatherDashboardData> {
  const { data } = await axios.get('/api/opendata/weather/dashboard', {
    params: { date, days: String(days) },
  });
  return data;
}

// --- BigQuery Open Data: Crypto Pulse ---

export async function fetchCryptoPulse(days: number): Promise<CryptoPulseData> {
  const { data } = await axios.get('/api/opendata/crypto/pulse', { params: { days: String(days) } });
  return data;
}

export async function fetchCryptoFees(days: number): Promise<CryptoFeesData> {
  const { data } = await axios.get('/api/opendata/crypto/fees', { params: { days: String(days) } });
  return data;
}

export async function fetchCryptoWhales(days: number, chain: CryptoChain): Promise<CryptoWhalesData> {
  const { data } = await axios.get('/api/opendata/crypto/whales', { params: { days: String(days), chain } });
  return data;
}

export async function fetchCryptoTokens(days: number): Promise<CryptoTokensData> {
  const { data } = await axios.get('/api/opendata/crypto/tokens', { params: { days: String(days) } });
  return data;
}

export async function fetchCryptoMining(days: number): Promise<CryptoMiningData> {
  const { data } = await axios.get('/api/opendata/crypto/mining', { params: { days: String(days) } });
  return data;
}

// Spot price is a convenience default for the mining calculator; on failure
// the tab falls back to manual input, so errors resolve to null here.
export async function fetchCryptoSpot(): Promise<CryptoSpotData | null> {
  try {
    const { data } = await axios.get('/api/opendata/crypto/spot');
    return data;
  } catch {
    return null;
  }
}

// --- BigQuery Open Data: GCP Billing ---

export function billingParams(f: BillingFilterState): Record<string, string> {
  const p: Record<string, string> = { dataset: f.dataset };
  if (f.invoiceMonth) {
    p.invoice_month = f.invoiceMonth;
  } else {
    p.start = f.start;
    p.end = f.end;
  }
  if (f.accounts.length) p.accounts = f.accounts.join(',');
  if (f.projects.length) p.projects = f.projects.join(',');
  if (f.services.length) p.services = f.services.join(',');
  if (f.labelKey && f.labelValue) p.label = `${f.labelKey}:${f.labelValue}`;
  return p;
}

export async function fetchBillingConfig(): Promise<BillingConfigResponse> {
  const { data } = await axios.get('/api/opendata/gcp_billing/config');
  return data;
}

export async function postBillingConfig(action: 'add' | 'remove', dataset: string): Promise<BillingConfigResponse> {
  const { data } = await axios.post('/api/opendata/gcp_billing/config', { action, dataset });
  return data;
}

export async function fetchBillingMeta(dataset: string): Promise<BillingMeta> {
  const { data } = await axios.get('/api/opendata/gcp_billing/meta', { params: { dataset } });
  return data;
}

export async function fetchBillingOverview(f: BillingFilterState): Promise<BillingOverviewData> {
  const { data } = await axios.get('/api/opendata/gcp_billing/overview', { params: billingParams(f) });
  return data;
}

export async function fetchBillingServices(f: BillingFilterState, service?: string): Promise<BillingServicesData> {
  const params = { ...billingParams(f), ...(service ? { service } : {}) };
  const { data } = await axios.get('/api/opendata/gcp_billing/services', { params });
  return data;
}

export async function fetchBillingProjects(f: BillingFilterState, groupLabel?: string): Promise<BillingProjectsData> {
  const params = { ...billingParams(f), ...(groupLabel ? { group_label: groupLabel } : {}) };
  const { data } = await axios.get('/api/opendata/gcp_billing/projects', { params });
  return data;
}

export async function fetchBillingResources(f: BillingFilterState, q?: string): Promise<BillingResourcesData> {
  const params = { ...billingParams(f), ...(q ? { q } : {}) };
  const { data } = await axios.get('/api/opendata/gcp_billing/resources', { params });
  return data;
}

export async function fetchBillingCredits(f: BillingFilterState): Promise<BillingCreditsData> {
  const { data } = await axios.get('/api/opendata/gcp_billing/credits', { params: billingParams(f) });
  return data;
}

export async function fetchBillingPricing(f: BillingFilterState, q?: string): Promise<BillingPricingData> {
  const params = { ...billingParams(f), ...(q ? { q } : {}) };
  const { data } = await axios.get('/api/opendata/gcp_billing/pricing', { params });
  return data;
}
