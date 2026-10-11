-- Seed data: 5 SKUs, 2 job-workers, 4 open issues.

INSERT INTO skus (code, name, image_url) VALUES
    ('KRT-001', 'Cotton Anarkali Kurta',     'https://picsum.photos/seed/krt001/200/260'),
    ('KRT-002', 'Chikankari Straight Kurta', 'https://picsum.photos/seed/krt002/200/260'),
    ('LHG-001', 'Zari Border Lehenga',       'https://picsum.photos/seed/lhg001/200/260'),
    ('DUP-001', 'Phulkari Dupatta',          'https://picsum.photos/seed/dup001/200/260'),
    ('SLW-001', 'Silk Salwar Set',           'https://picsum.photos/seed/slw001/200/260');

INSERT INTO parties (name, phone) VALUES
    ('Ramesh Embroidery Works', '+91 98100 11111'),
    ('Sunita Stitching Unit',   '+91 98100 22222');

-- rate_paise is the job-work charge per piece (e.g. 18000 = Rs 180.00).
INSERT INTO jobwork_issues (party_id, sku_id, qty, rate_paise, issued_on)
SELECT p.id, s.id, v.qty, v.rate_paise, v.issued_on::date
FROM (VALUES
    ('Ramesh Embroidery Works', 'KRT-001', 100, 18000, '2026-10-01'),
    ('Ramesh Embroidery Works', 'LHG-001',  40, 95000, '2026-10-02'),
    ('Sunita Stitching Unit',   'KRT-002',  60, 12000, '2026-10-03'),
    ('Sunita Stitching Unit',   'DUP-001',  80,  6500, '2026-10-04')
) AS v (party, sku, qty, rate_paise, issued_on)
JOIN parties p ON p.name = v.party
JOIN skus s    ON s.code = v.sku;
