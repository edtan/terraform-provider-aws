package aws

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/endpoints"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/hashicorp/terraform/helper/hashcode"
	"github.com/hashicorp/terraform/helper/resource"
	"github.com/hashicorp/terraform/helper/schema"
	"github.com/hashicorp/terraform/helper/structure"
	"github.com/hashicorp/terraform/helper/validation"
)

func resourceAwsS3Bucket() *schema.Resource {
	return &schema.Resource{
		Create: resourceAwsS3BucketCreate,
		Read:   resourceAwsS3BucketRead,
		Update: resourceAwsS3BucketUpdate,
		Delete: resourceAwsS3BucketDelete,
		Importer: &schema.ResourceImporter{
			State: resourceAwsS3BucketImportState,
		},

		Schema: map[string]*schema.Schema{
			"bucket": {
				Type:          schema.TypeString,
				Optional:      true,
				Computed:      true,
				ForceNew:      true,
				ConflictsWith: []string{"bucket_prefix"},
				ValidateFunc:  validation.StringLenBetween(0, 63),
			},
			"bucket_prefix": {
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      true,
				ConflictsWith: []string{"bucket"},
				ValidateFunc:  validation.StringLenBetween(0, 63-resource.UniqueIDSuffixLength),
			},

			"bucket_domain_name": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"bucket_regional_domain_name": {
				Type:     schema.TypeString,
				Computed: true,
			},

			"arn": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},

			"acl": {
				Type:     schema.TypeString,
				Default:  "private",
				Optional: true,
			},

			"policy": {
				Type:             schema.TypeString,
				Optional:         true,
				ValidateFunc:     validation.ValidateJsonString,
				DiffSuppressFunc: suppressEquivalentAwsPolicyDiffs,
			},

			"cors_rule": {
				Type:     schema.TypeList,
				Optional: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"allowed_headers": {
							Type:     schema.TypeList,
							Optional: true,
							Elem:     &schema.Schema{Type: schema.TypeString},
						},
						"allowed_methods": {
							Type:     schema.TypeList,
							Required: true,
							Elem:     &schema.Schema{Type: schema.TypeString},
						},
						"allowed_origins": {
							Type:     schema.TypeList,
							Required: true,
							Elem:     &schema.Schema{Type: schema.TypeString},
						},
						"expose_headers": {
							Type:     schema.TypeList,
							Optional: true,
							Elem:     &schema.Schema{Type: schema.TypeString},
						},
						"max_age_seconds": {
							Type:     schema.TypeInt,
							Optional: true,
						},
					},
				},
			},

			"website": {
				Type:     schema.TypeList,
				Optional: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"index_document": {
							Type:     schema.TypeString,
							Optional: true,
						},

						"error_document": {
							Type:     schema.TypeString,
							Optional: true,
						},

						"redirect_all_requests_to": {
							Type: schema.TypeString,
							ConflictsWith: []string{
								"website.0.index_document",
								"website.0.error_document",
								"website.0.routing_rules",
							},
							Optional: true,
						},

						"routing_rules": {
							Type:         schema.TypeString,
							Optional:     true,
							ValidateFunc: validation.ValidateJsonString,
							StateFunc: func(v interface{}) string {
								json, _ := structure.NormalizeJsonString(v)
								return json
							},
						},
					},
				},
			},

			"hosted_zone_id": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},

			"region": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},
			"website_endpoint": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},
			"website_domain": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
			},

			"versioning": {
				Type:     schema.TypeList,
				Optional: true,
				Computed: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"enabled": {
							Type:     schema.TypeBool,
							Optional: true,
							Default:  false,
						},
						"mfa_delete": {
							Type:     schema.TypeBool,
							Optional: true,
							Default:  false,
						},
					},
				},
			},

			"logging": {
				Type:     schema.TypeSet,
				Optional: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"target_bucket": {
							Type:     schema.TypeString,
							Required: true,
						},
						"target_prefix": {
							Type:     schema.TypeString,
							Optional: true,
						},
					},
				},
				Set: func(v interface{}) int {
					var buf bytes.Buffer
					m := v.(map[string]interface{})
					buf.WriteString(fmt.Sprintf("%s-", m["target_bucket"]))
					buf.WriteString(fmt.Sprintf("%s-", m["target_prefix"]))
					return hashcode.String(buf.String())
				},
			},

			"lifecycle_rule": {
				Type:     schema.TypeList,
				Optional: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"id": {
							Type:         schema.TypeString,
							Optional:     true,
							Computed:     true,
							ValidateFunc: validation.StringLenBetween(0, 255),
						},
						"prefix": {
							Type:     schema.TypeString,
							Optional: true,
						},
						"tags": tagsSchema(),
						"enabled": {
							Type:     schema.TypeBool,
							Required: true,
						},
						"abort_incomplete_multipart_upload_days": {
							Type:     schema.TypeInt,
							Optional: true,
						},
						"expiration": {
							Type:     schema.TypeSet,
							Optional: true,
							Set:      expirationHash,
							MaxItems: 1,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"date": {
										Type:         schema.TypeString,
										Optional:     true,
										ValidateFunc: validateS3BucketLifecycleTimestamp,
									},
									"days": {
										Type:         schema.TypeInt,
										Optional:     true,
										ValidateFunc: validation.IntAtLeast(0),
									},
									"expired_object_delete_marker": {
										Type:     schema.TypeBool,
										Optional: true,
									},
								},
							},
						},
						"noncurrent_version_expiration": {
							Type:     schema.TypeSet,
							MaxItems: 1,
							Optional: true,
							Set:      expirationHash,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"days": {
										Type:         schema.TypeInt,
										Optional:     true,
										ValidateFunc: validation.IntAtLeast(1),
									},
								},
							},
						},
						"transition": {
							Type:     schema.TypeSet,
							Optional: true,
							Set:      transitionHash,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"date": {
										Type:         schema.TypeString,
										Optional:     true,
										ValidateFunc: validateS3BucketLifecycleTimestamp,
									},
									"days": {
										Type:         schema.TypeInt,
										Optional:     true,
										ValidateFunc: validation.IntAtLeast(0),
									},
									"storage_class": {
										Type:         schema.TypeString,
										Required:     true,
										ValidateFunc: validateS3BucketLifecycleTransitionStorageClass(),
									},
								},
							},
						},
						"noncurrent_version_transition": {
							Type:     schema.TypeSet,
							Optional: true,
							Set:      transitionHash,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"days": {
										Type:         schema.TypeInt,
										Optional:     true,
										ValidateFunc: validation.IntAtLeast(0),
									},
									"storage_class": {
										Type:         schema.TypeString,
										Required:     true,
										ValidateFunc: validateS3BucketLifecycleTransitionStorageClass(),
									},
								},
							},
						},
					},
				},
			},

			"force_destroy": {
				Type:     schema.TypeBool,
				Optional: true,
				Default:  false,
			},

			"acceleration_status": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ValidateFunc: validation.StringInSlice([]string{
					s3.BucketAccelerateStatusEnabled,
					s3.BucketAccelerateStatusSuspended,
				}, false),
			},

			"request_payer": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				ValidateFunc: validation.StringInSlice([]string{
					s3.PayerRequester,
					s3.PayerBucketOwner,
				}, false),
			},

			"replication_configuration": {
				Type:     schema.TypeList,
				Optional: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"role": {
							Type:     schema.TypeString,
							Required: true,
						},
						"rules": {
							Type:     schema.TypeSet,
							Required: true,
							Set:      rulesHash,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"id": {
										Type:         schema.TypeString,
										Optional:     true,
										ValidateFunc: validation.StringLenBetween(0, 255),
									},
									"destination": {
										Type:     schema.TypeSet,
										MaxItems: 1,
										MinItems: 1,
										Required: true,
										Set:      destinationHash,
										Elem: &schema.Resource{
											Schema: map[string]*schema.Schema{
												"account_id": {
													Type:         schema.TypeString,
													Optional:     true,
													ValidateFunc: validateAwsAccountId,
												},
												"bucket": {
													Type:         schema.TypeString,
													Required:     true,
													ValidateFunc: validateArn,
												},
												"storage_class": {
													Type:     schema.TypeString,
													Optional: true,
													ValidateFunc: validation.StringInSlice([]string{
														s3.StorageClassStandard,
														s3.StorageClassReducedRedundancy,
														s3.StorageClassStandardIa,
														s3.StorageClassOnezoneIa,
														s3.StorageClassIntelligentTiering,
														s3.StorageClassGlacier,
														s3.StorageClassDeepArchive,
													}, false),
												},
												"replica_kms_key_id": {
													Type:     schema.TypeString,
													Optional: true,
												},
												"access_control_translation": {
													Type:     schema.TypeList,
													Optional: true,
													MinItems: 1,
													MaxItems: 1,
													Elem: &schema.Resource{
														Schema: map[string]*schema.Schema{
															"owner": {
																Type:     schema.TypeString,
																Required: true,
																ValidateFunc: validation.StringInSlice([]string{
																	s3.OwnerOverrideDestination,
																}, false),
															},
														},
													},
												},
											},
										},
									},
									"source_selection_criteria": {
										Type:     schema.TypeSet,
										Optional: true,
										MinItems: 1,
										MaxItems: 1,
										Set:      sourceSelectionCriteriaHash,
										Elem: &schema.Resource{
											Schema: map[string]*schema.Schema{
												"sse_kms_encrypted_objects": {
													Type:     schema.TypeSet,
													Optional: true,
													MinItems: 1,
													MaxItems: 1,
													Set:      sourceSseKmsObjectsHash,
													Elem: &schema.Resource{
														Schema: map[string]*schema.Schema{
															"enabled": {
																Type:     schema.TypeBool,
																Required: true,
															},
														},
													},
												},
											},
										},
									},
									"prefix": {
										Type:         schema.TypeString,
										Optional:     true,
										ValidateFunc: validation.StringLenBetween(0, 1024),
									},
									"status": {
										Type:     schema.TypeString,
										Required: true,
										ValidateFunc: validation.StringInSlice([]string{
											s3.ReplicationRuleStatusEnabled,
											s3.ReplicationRuleStatusDisabled,
										}, false),
									},
									"priority": {
										Type:     schema.TypeInt,
										Optional: true,
									},
									"filter": {
										Type:     schema.TypeList,
										Optional: true,
										MinItems: 1,
										MaxItems: 1,
										Elem: &schema.Resource{
											Schema: map[string]*schema.Schema{
												"prefix": {
													Type:         schema.TypeString,
													Optional:     true,
													ValidateFunc: validation.StringLenBetween(0, 1024),
												},
												"tags": tagsSchema(),
											},
										},
									},
								},
							},
						},
					},
				},
			},

			"server_side_encryption_configuration": {
				Type:     schema.TypeList,
				MaxItems: 1,
				Optional: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"rule": {
							Type:     schema.TypeList,
							MaxItems: 1,
							Required: true,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"apply_server_side_encryption_by_default": {
										Type:     schema.TypeList,
										MaxItems: 1,
										Required: true,
										Elem: &schema.Resource{
											Schema: map[string]*schema.Schema{
												"kms_master_key_id": {
													Type:     schema.TypeString,
													Optional: true,
												},
												"sse_algorithm": {
													Type:     schema.TypeString,
													Required: true,
													ValidateFunc: validation.StringInSlice([]string{
														s3.ServerSideEncryptionAes256,
														s3.ServerSideEncryptionAwsKms,
													}, false),
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},

			"object_lock_configuration": {
				Type:     schema.TypeList,
				Optional: true,
				MaxItems: 1,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"object_lock_enabled": {
							Type:     schema.TypeString,
							Required: true,
							ForceNew: true,
							ValidateFunc: validation.StringInSlice([]string{
								s3.ObjectLockEnabledEnabled,
							}, false),
						},

						"rule": {
							Type:     schema.TypeList,
							Optional: true,
							MaxItems: 1,
							Elem: &schema.Resource{
								Schema: map[string]*schema.Schema{
									"default_retention": {
										Type:     schema.TypeList,
										Required: true,
										MinItems: 1,
										MaxItems: 1,
										Elem: &schema.Resource{
											Schema: map[string]*schema.Schema{
												"mode": {
													Type:     schema.TypeString,
													Required: true,
													ValidateFunc: validation.StringInSlice([]string{
														s3.ObjectLockModeGovernance,
														s3.ObjectLockModeCompliance,
													}, false),
												},

												"days": {
													Type:         schema.TypeInt,
													Optional:     true,
													ValidateFunc: validation.IntAtLeast(1),
												},

												"years": {
													Type:         schema.TypeInt,
													Optional:     true,
													ValidateFunc: validation.IntAtLeast(1),
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},

			"tags": tagsSchema(),
		},
	}
}

func resourceAwsS3BucketCreate(d *schema.ResourceData, meta interface{}) error {
	s3conn := meta.(*AWSClient).s3conn

	// Get the bucket and acl
	var bucket string
	if v, ok := d.GetOk("bucket"); ok {
		bucket = v.(string)
	} else if v, ok := d.GetOk("bucket_prefix"); ok {
		bucket = resource.PrefixedUniqueId(v.(string))
	} else {
		bucket = resource.UniqueId()
	}
	d.Set("bucket", bucket)
	acl := d.Get("acl").(string)

	log.Printf("[DEBUG] S3 bucket create: %s, ACL: %s", bucket, acl)

	req := &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
		ACL:    aws.String(acl),
	}

	var awsRegion string
	if region, ok := d.GetOk("region"); ok {
		awsRegion = region.(string)
	} else {
		awsRegion = meta.(*AWSClient).region
	}
	log.Printf("[DEBUG] S3 bucket create: %s, using region: %s", bucket, awsRegion)

	// Special case us-east-1 region and do not set the LocationConstraint.
	// See "Request Elements: http://docs.aws.amazon.com/AmazonS3/latest/API/RESTBucketPUT.html
	if awsRegion != "us-east-1" {
		req.CreateBucketConfiguration = &s3.CreateBucketConfiguration{
			LocationConstraint: aws.String(awsRegion),
		}
	}

	if err := validateS3BucketName(bucket, awsRegion); err != nil {
		return fmt.Errorf("Error validating S3 bucket name: %s", err)
	}

	// S3 Object Lock can only be enabled on bucket creation.
	objectLockConfiguration := expandS3ObjectLockConfiguration(d.Get("object_lock_configuration").([]interface{}))
	if objectLockConfiguration != nil && aws.StringValue(objectLockConfiguration.ObjectLockEnabled) == s3.ObjectLockEnabledEnabled {
		req.ObjectLockEnabledForBucket = aws.Bool(true)
	}

	err := resource.Retry(5*time.Minute, func() *resource.RetryError {
		log.Printf("[DEBUG] Trying to create new S3 bucket: %q", bucket)
		_, err := s3conn.CreateBucket(req)
		if awsErr, ok := err.(awserr.Error); ok {
			if awsErr.Code() == "OperationAborted" {
				log.Printf("[WARN] Got an error while trying to create S3 bucket %s: %s", bucket, err)
				return resource.RetryableError(
					fmt.Errorf("Error creating S3 bucket %s, retrying: %s", bucket, err))
			}
		}
		if err != nil {
			return resource.NonRetryableError(err)
		}

		return nil
	})
	if isResourceTimeoutError(err) {
		_, err = s3conn.CreateBucket(req)
	}
	if err != nil {
		return fmt.Errorf("Error creating S3 bucket: %s", err)
	}

	// Assign the bucket name as the resource ID
	d.SetId(bucket)
	return resourceAwsS3BucketUpdate(d, meta)
}

func resourceAwsS3BucketUpdate(d *schema.ResourceData, meta interface{}) error {
	s3conn := meta.(*AWSClient).s3conn
	if err := setTagsS3(s3conn, d); err != nil {
		return fmt.Errorf("%q: %s", d.Get("bucket").(string), err)
	}

	if d.HasChange("policy") {
		if err := resourceAwsS3BucketPolicyUpdate(s3conn, d); err != nil {
			return err
		}
	}

	if d.HasChange("cors_rule") {
		if err := resourceAwsS3BucketCorsUpdate(s3conn, d); err != nil {
			return err
		}
	}

	if d.HasChange("website") {
		if err := resourceAwsS3BucketWebsiteUpdate(s3conn, d); err != nil {
			return err
		}
	}

	if d.HasChange("versioning") {
		if err := resourceAwsS3BucketVersioningUpdate(s3conn, d); err != nil {
			return err
		}
	}
	if d.HasChange("acl") && !d.IsNewResource() {
		if err := resourceAwsS3BucketAclUpdate(s3conn, d); err != nil {
			return err
		}
	}

	if d.HasChange("logging") {
		if err := resourceAwsS3BucketLoggingUpdate(s3conn, d); err != nil {
			return err
		}
	}

	if d.HasChange("lifecycle_rule") {
		if err := resourceAwsS3BucketLifecycleUpdate(s3conn, d); err != nil {
			return err
		}
	}

	if d.HasChange("acceleration_status") {
		if err := resourceAwsS3BucketAccelerationUpdate(s3conn, d); err != nil {
			return err
		}
	}

	if d.HasChange("request_payer") {
		if err := resourceAwsS3BucketRequestPayerUpdate(s3conn, d); err != nil {
			return err
		}
	}

	if d.HasChange("replication_configuration") {
		if err := resourceAwsS3BucketReplicationConfigurationUpdate(s3conn, d); err != nil {
			return err
		}
	}

	if d.HasChange("server_side_encryption_configuration") {
		if err := resourceAwsS3BucketServerSideEncryptionConfigurationUpdate(s3conn, d); err != nil {
			return err
		}
	}

	if d.HasChange("object_lock_configuration") {
		if err := resourceAwsS3BucketObjectLockConfigurationUpdate(s3conn, d); err != nil {
			return err
		}
	}

	return resourceAwsS3BucketRead(d, meta)
}

func resourceAwsS3BucketRead(d *schema.ResourceData, meta interface{}) error {
	s3conn := meta.(*AWSClient).s3conn

	var err error

	_, err = retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.HeadBucket(&s3.HeadBucketInput{
			Bucket: aws.String(d.Id()),
		})
	})
	if err != nil {
		if awsError, ok := err.(awserr.RequestFailure); ok && awsError.StatusCode() == 404 {
			log.Printf("[WARN] S3 Bucket (%s) not found, error code (404)", d.Id())
			d.SetId("")
			return nil
		}
		return fmt.Errorf("error reading S3 Bucket (%s): %s", d.Id(), err)
	}

	// In the import case, we won't have this
	if _, ok := d.GetOk("bucket"); !ok {
		d.Set("bucket", d.Id())
	}

	d.Set("bucket_domain_name", bucketDomainName(d.Get("bucket").(string)))

	// Read the policy
	if _, ok := d.GetOk("policy"); ok {

		pol, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
			return s3conn.GetBucketPolicy(&s3.GetBucketPolicyInput{
				Bucket: aws.String(d.Id()),
			})
		})
		log.Printf("[DEBUG] S3 bucket: %s, read policy: %v", d.Id(), pol)
		if err != nil {
			if err := d.Set("policy", ""); err != nil {
				return err
			}
		} else {
			if v := pol.(*s3.GetBucketPolicyOutput).Policy; v == nil {
				if err := d.Set("policy", ""); err != nil {
					return err
				}
			} else {
				policy, err := structure.NormalizeJsonString(*v)
				if err != nil {
					return fmt.Errorf("policy contains an invalid JSON: %s", err)
				}
				d.Set("policy", policy)
			}
		}
	}

	// Read the CORS
	corsResponse, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.GetBucketCors(&s3.GetBucketCorsInput{
			Bucket: aws.String(d.Id()),
		})
	})
	if err != nil && !isAWSErr(err, "NoSuchCORSConfiguration", "") {
		return fmt.Errorf("error getting S3 Bucket CORS configuration: %s", err)
	}

	corsRules := make([]map[string]interface{}, 0)
	if cors, ok := corsResponse.(*s3.GetBucketCorsOutput); ok && len(cors.CORSRules) > 0 {
		corsRules = make([]map[string]interface{}, 0, len(cors.CORSRules))
		for _, ruleObject := range cors.CORSRules {
			rule := make(map[string]interface{})
			rule["allowed_headers"] = flattenStringList(ruleObject.AllowedHeaders)
			rule["allowed_methods"] = flattenStringList(ruleObject.AllowedMethods)
			rule["allowed_origins"] = flattenStringList(ruleObject.AllowedOrigins)
			// Both the "ExposeHeaders" and "MaxAgeSeconds" might not be set.
			if ruleObject.AllowedOrigins != nil {
				rule["expose_headers"] = flattenStringList(ruleObject.ExposeHeaders)
			}
			if ruleObject.MaxAgeSeconds != nil {
				rule["max_age_seconds"] = int(*ruleObject.MaxAgeSeconds)
			}
			corsRules = append(corsRules, rule)
		}
	}
	if err := d.Set("cors_rule", corsRules); err != nil {
		return fmt.Errorf("error setting cors_rule: %s", err)
	}

	// Read the website configuration
	wsResponse, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.GetBucketWebsite(&s3.GetBucketWebsiteInput{
			Bucket: aws.String(d.Id()),
		})
	})
	if err != nil && !isAWSErr(err, "NotImplemented", "") && !isAWSErr(err, "NoSuchWebsiteConfiguration", "") {
		return fmt.Errorf("error getting S3 Bucket website configuration: %s", err)
	}

	websites := make([]map[string]interface{}, 0, 1)
	if ws, ok := wsResponse.(*s3.GetBucketWebsiteOutput); ok {
		w := make(map[string]interface{})

		if v := ws.IndexDocument; v != nil {
			w["index_document"] = *v.Suffix
		}

		if v := ws.ErrorDocument; v != nil {
			w["error_document"] = *v.Key
		}

		if v := ws.RedirectAllRequestsTo; v != nil {
			if v.Protocol == nil {
				w["redirect_all_requests_to"] = *v.HostName
			} else {
				var host string
				var path string
				var query string
				parsedHostName, err := url.Parse(*v.HostName)
				if err == nil {
					host = parsedHostName.Host
					path = parsedHostName.Path
					query = parsedHostName.RawQuery
				} else {
					host = *v.HostName
					path = ""
				}

				w["redirect_all_requests_to"] = (&url.URL{
					Host:     host,
					Path:     path,
					Scheme:   *v.Protocol,
					RawQuery: query,
				}).String()
			}
		}

		if v := ws.RoutingRules; v != nil {
			rr, err := normalizeRoutingRules(v)
			if err != nil {
				return fmt.Errorf("Error while marshaling routing rules: %s", err)
			}
			w["routing_rules"] = rr
		}

		// We have special handling for the website configuration,
		// so only add the configuration if there is any
		if len(w) > 0 {
			websites = append(websites, w)
		}
	}
	if err := d.Set("website", websites); err != nil {
		return fmt.Errorf("error setting website: %s", err)
	}

	// Read the versioning configuration

	versioningResponse, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.GetBucketVersioning(&s3.GetBucketVersioningInput{
			Bucket: aws.String(d.Id()),
		})
	})
	if err != nil {
		return err
	}

	vcl := make([]map[string]interface{}, 0, 1)
	if versioning, ok := versioningResponse.(*s3.GetBucketVersioningOutput); ok {
		vc := make(map[string]interface{})
		if versioning.Status != nil && *versioning.Status == s3.BucketVersioningStatusEnabled {
			vc["enabled"] = true
		} else {
			vc["enabled"] = false
		}

		if versioning.MFADelete != nil && *versioning.MFADelete == s3.MFADeleteEnabled {
			vc["mfa_delete"] = true
		} else {
			vc["mfa_delete"] = false
		}
		vcl = append(vcl, vc)
	}
	if err := d.Set("versioning", vcl); err != nil {
		return fmt.Errorf("error setting versioning: %s", err)
	}

	// Read the acceleration status

	accelerateResponse, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.GetBucketAccelerateConfiguration(&s3.GetBucketAccelerateConfigurationInput{
			Bucket: aws.String(d.Id()),
		})
	})

	// Amazon S3 Transfer Acceleration might not be supported in the region
	if err != nil && !isAWSErr(err, "MethodNotAllowed", "") && !isAWSErr(err, "UnsupportedArgument", "") {
		return fmt.Errorf("error getting S3 Bucket acceleration configuration: %s", err)
	}
	if accelerate, ok := accelerateResponse.(*s3.GetBucketAccelerateConfigurationOutput); ok {
		d.Set("acceleration_status", accelerate.Status)
	}

	// Read the request payer configuration.

	payerResponse, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.GetBucketRequestPayment(&s3.GetBucketRequestPaymentInput{
			Bucket: aws.String(d.Id()),
		})
	})

	if err != nil {
		return fmt.Errorf("error getting S3 Bucket request payment: %s", err)
	}

	if payer, ok := payerResponse.(*s3.GetBucketRequestPaymentOutput); ok {
		d.Set("request_payer", payer.Payer)
	}

	// Read the logging configuration
	loggingResponse, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.GetBucketLogging(&s3.GetBucketLoggingInput{
			Bucket: aws.String(d.Id()),
		})
	})

	if err != nil {
		return fmt.Errorf("error getting S3 Bucket logging: %s", err)
	}

	lcl := make([]map[string]interface{}, 0, 1)
	if logging, ok := loggingResponse.(*s3.GetBucketLoggingOutput); ok && logging.LoggingEnabled != nil {
		v := logging.LoggingEnabled
		lc := make(map[string]interface{})
		if *v.TargetBucket != "" {
			lc["target_bucket"] = *v.TargetBucket
		}
		if *v.TargetPrefix != "" {
			lc["target_prefix"] = *v.TargetPrefix
		}
		lcl = append(lcl, lc)
	}
	if err := d.Set("logging", lcl); err != nil {
		return fmt.Errorf("error setting logging: %s", err)
	}

	// Read the lifecycle configuration

	lifecycleResponse, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.GetBucketLifecycleConfiguration(&s3.GetBucketLifecycleConfigurationInput{
			Bucket: aws.String(d.Id()),
		})
	})
	if err != nil && !isAWSErr(err, "NoSuchLifecycleConfiguration", "") {
		return err
	}

	lifecycleRules := make([]map[string]interface{}, 0)
	if lifecycle, ok := lifecycleResponse.(*s3.GetBucketLifecycleConfigurationOutput); ok && len(lifecycle.Rules) > 0 {
		lifecycleRules = make([]map[string]interface{}, 0, len(lifecycle.Rules))

		for _, lifecycleRule := range lifecycle.Rules {
			log.Printf("[DEBUG] S3 bucket: %s, read lifecycle rule: %v", d.Id(), lifecycleRule)
			rule := make(map[string]interface{})

			// ID
			if lifecycleRule.ID != nil && *lifecycleRule.ID != "" {
				rule["id"] = *lifecycleRule.ID
			}
			filter := lifecycleRule.Filter
			if filter != nil {
				if filter.And != nil {
					// Prefix
					if filter.And.Prefix != nil && *filter.And.Prefix != "" {
						rule["prefix"] = *filter.And.Prefix
					}
					// Tag
					if len(filter.And.Tags) > 0 {
						rule["tags"] = tagsToMapS3(filter.And.Tags)
					}
				} else {
					// Prefix
					if filter.Prefix != nil && *filter.Prefix != "" {
						rule["prefix"] = *filter.Prefix
					}
					// Tag
					if filter.Tag != nil {
						rule["tags"] = tagsToMapS3([]*s3.Tag{filter.Tag})
					}
				}
			} else {
				if lifecycleRule.Prefix != nil {
					rule["prefix"] = *lifecycleRule.Prefix
				}
			}

			// Enabled
			if lifecycleRule.Status != nil {
				if *lifecycleRule.Status == s3.ExpirationStatusEnabled {
					rule["enabled"] = true
				} else {
					rule["enabled"] = false
				}
			}

			// AbortIncompleteMultipartUploadDays
			if lifecycleRule.AbortIncompleteMultipartUpload != nil {
				if lifecycleRule.AbortIncompleteMultipartUpload.DaysAfterInitiation != nil {
					rule["abort_incomplete_multipart_upload_days"] = int(*lifecycleRule.AbortIncompleteMultipartUpload.DaysAfterInitiation)
				}
			}

			// expiration
			if lifecycleRule.Expiration != nil {
				e := make(map[string]interface{})
				if lifecycleRule.Expiration.Date != nil {
					e["date"] = (*lifecycleRule.Expiration.Date).Format("2006-01-02")
				}
				if lifecycleRule.Expiration.Days != nil {
					e["days"] = int(*lifecycleRule.Expiration.Days)
				}
				if lifecycleRule.Expiration.ExpiredObjectDeleteMarker != nil {
					e["expired_object_delete_marker"] = *lifecycleRule.Expiration.ExpiredObjectDeleteMarker
				}
				rule["expiration"] = schema.NewSet(expirationHash, []interface{}{e})
			}
			// noncurrent_version_expiration
			if lifecycleRule.NoncurrentVersionExpiration != nil {
				e := make(map[string]interface{})
				if lifecycleRule.NoncurrentVersionExpiration.NoncurrentDays != nil {
					e["days"] = int(*lifecycleRule.NoncurrentVersionExpiration.NoncurrentDays)
				}
				rule["noncurrent_version_expiration"] = schema.NewSet(expirationHash, []interface{}{e})
			}
			//// transition
			if len(lifecycleRule.Transitions) > 0 {
				transitions := make([]interface{}, 0, len(lifecycleRule.Transitions))
				for _, v := range lifecycleRule.Transitions {
					t := make(map[string]interface{})
					if v.Date != nil {
						t["date"] = (*v.Date).Format("2006-01-02")
					}
					if v.Days != nil {
						t["days"] = int(*v.Days)
					}
					if v.StorageClass != nil {
						t["storage_class"] = *v.StorageClass
					}
					transitions = append(transitions, t)
				}
				rule["transition"] = schema.NewSet(transitionHash, transitions)
			}
			// noncurrent_version_transition
			if len(lifecycleRule.NoncurrentVersionTransitions) > 0 {
				transitions := make([]interface{}, 0, len(lifecycleRule.NoncurrentVersionTransitions))
				for _, v := range lifecycleRule.NoncurrentVersionTransitions {
					t := make(map[string]interface{})
					if v.NoncurrentDays != nil {
						t["days"] = int(*v.NoncurrentDays)
					}
					if v.StorageClass != nil {
						t["storage_class"] = *v.StorageClass
					}
					transitions = append(transitions, t)
				}
				rule["noncurrent_version_transition"] = schema.NewSet(transitionHash, transitions)
			}

			lifecycleRules = append(lifecycleRules, rule)
		}
	}
	if err := d.Set("lifecycle_rule", lifecycleRules); err != nil {
		return fmt.Errorf("error setting lifecycle_rule: %s", err)
	}

	// Read the bucket replication configuration

	replicationResponse, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.GetBucketReplication(&s3.GetBucketReplicationInput{
			Bucket: aws.String(d.Id()),
		})
	})
	if err != nil && !isAWSErr(err, "ReplicationConfigurationNotFoundError", "") {
		return fmt.Errorf("error getting S3 Bucket replication: %s", err)
	}

	replicationConfiguration := make([]map[string]interface{}, 0)
	if replication, ok := replicationResponse.(*s3.GetBucketReplicationOutput); ok {
		replicationConfiguration = flattenAwsS3BucketReplicationConfiguration(replication.ReplicationConfiguration)
	}
	if err := d.Set("replication_configuration", replicationConfiguration); err != nil {
		return fmt.Errorf("error setting replication_configuration: %s", err)
	}

	// Read the bucket server side encryption configuration

	encryptionResponse, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.GetBucketEncryption(&s3.GetBucketEncryptionInput{
			Bucket: aws.String(d.Id()),
		})
	})
	if err != nil && !isAWSErr(err, "ServerSideEncryptionConfigurationNotFoundError", "encryption configuration was not found") {
		return fmt.Errorf("error getting S3 Bucket encryption: %s", err)
	}

	serverSideEncryptionConfiguration := make([]map[string]interface{}, 0)
	if encryption, ok := encryptionResponse.(*s3.GetBucketEncryptionOutput); ok && encryption.ServerSideEncryptionConfiguration != nil {
		serverSideEncryptionConfiguration = flattenAwsS3ServerSideEncryptionConfiguration(encryption.ServerSideEncryptionConfiguration)
	}
	if err := d.Set("server_side_encryption_configuration", serverSideEncryptionConfiguration); err != nil {
		return fmt.Errorf("error setting server_side_encryption_configuration: %s", err)
	}

	// Object Lock configuration.
	if conf, err := readS3ObjectLockConfiguration(s3conn, d.Id()); err != nil {
		return fmt.Errorf("error getting S3 Bucket Object Lock configuration: %s", err)
	} else {
		if err := d.Set("object_lock_configuration", conf); err != nil {
			return fmt.Errorf("error setting object_lock_configuration: %s", err)
		}
	}

	// Add the region as an attribute

	locationResponse, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.GetBucketLocation(
			&s3.GetBucketLocationInput{
				Bucket: aws.String(d.Id()),
			},
		)
	})
	if err != nil {
		return fmt.Errorf("error getting S3 Bucket location: %s", err)
	}

	var region string
	if location, ok := locationResponse.(*s3.GetBucketLocationOutput); ok && location.LocationConstraint != nil {
		region = *location.LocationConstraint
	}
	region = normalizeRegion(region)
	if err := d.Set("region", region); err != nil {
		return err
	}

	// Add the bucket_regional_domain_name as an attribute
	regionalEndpoint, err := BucketRegionalDomainName(d.Get("bucket").(string), region)
	if err != nil {
		return err
	}
	d.Set("bucket_regional_domain_name", regionalEndpoint)

	// Add the hosted zone ID for this bucket's region as an attribute
	hostedZoneID, err := HostedZoneIDForRegion(region)
	if err != nil {
		log.Printf("[WARN] %s", err)
	} else {
		d.Set("hosted_zone_id", hostedZoneID)
	}

	// Add website_endpoint as an attribute
	websiteEndpoint, err := websiteEndpoint(s3conn, d)
	if err != nil {
		return err
	}
	if websiteEndpoint != nil {
		if err := d.Set("website_endpoint", websiteEndpoint.Endpoint); err != nil {
			return err
		}
		if err := d.Set("website_domain", websiteEndpoint.Domain); err != nil {
			return err
		}
	}

	tagSet, err := getTagSetS3(s3conn, d.Id())
	if err != nil {
		return err
	}

	if err := d.Set("tags", tagsToMapS3(tagSet)); err != nil {
		return err
	}

	arn := arn.ARN{
		Partition: meta.(*AWSClient).partition,
		Service:   "s3",
		Resource:  d.Id(),
	}.String()
	d.Set("arn", arn)

	return nil
}

func resourceAwsS3BucketDelete(d *schema.ResourceData, meta interface{}) error {
	s3conn := meta.(*AWSClient).s3conn

	log.Printf("[DEBUG] S3 Delete Bucket: %s", d.Id())
	_, err := s3conn.DeleteBucket(&s3.DeleteBucketInput{
		Bucket: aws.String(d.Id()),
	})

	if isAWSErr(err, s3.ErrCodeNoSuchBucket, "") {
		return nil
	}

	if isAWSErr(err, "BucketNotEmpty", "") {
		if d.Get("force_destroy").(bool) {
			// bucket may have things delete them
			log.Printf("[DEBUG] S3 Bucket attempting to forceDestroy %+v", err)

			bucket := d.Get("bucket").(string)
			resp, err := s3conn.ListObjectVersions(
				&s3.ListObjectVersionsInput{
					Bucket: aws.String(bucket),
				},
			)

			if err != nil {
				return fmt.Errorf("Error S3 Bucket list Object Versions err: %s", err)
			}

			objectsToDelete := make([]*s3.ObjectIdentifier, 0)

			if len(resp.DeleteMarkers) != 0 {

				for _, v := range resp.DeleteMarkers {
					objectsToDelete = append(objectsToDelete, &s3.ObjectIdentifier{
						Key:       v.Key,
						VersionId: v.VersionId,
					})
				}
			}

			if len(resp.Versions) != 0 {
				for _, v := range resp.Versions {
					objectsToDelete = append(objectsToDelete, &s3.ObjectIdentifier{
						Key:       v.Key,
						VersionId: v.VersionId,
					})
				}
			}

			params := &s3.DeleteObjectsInput{
				Bucket: aws.String(bucket),
				Delete: &s3.Delete{
					Objects: objectsToDelete,
				},
			}

			_, err = s3conn.DeleteObjects(params)

			if err != nil {
				return fmt.Errorf("Error S3 Bucket force_destroy error deleting: %s", err)
			}

			// this line recurses until all objects are deleted or an error is returned
			return resourceAwsS3BucketDelete(d, meta)
		}
	}

	if err != nil {
		return fmt.Errorf("error deleting S3 Bucket (%s): %s", d.Id(), err)
	}

	return nil
}

func resourceAwsS3BucketPolicyUpdate(s3conn *s3.S3, d *schema.ResourceData) error {
	bucket := d.Get("bucket").(string)
	policy := d.Get("policy").(string)

	if policy != "" {
		log.Printf("[DEBUG] S3 bucket: %s, put policy: %s", bucket, policy)

		params := &s3.PutBucketPolicyInput{
			Bucket: aws.String(bucket),
			Policy: aws.String(policy),
		}

		err := resource.Retry(1*time.Minute, func() *resource.RetryError {
			_, err := s3conn.PutBucketPolicy(params)
			if isAWSErr(err, "MalformedPolicy", "") || isAWSErr(err, s3.ErrCodeNoSuchBucket, "") {
				return resource.RetryableError(err)
			}
			if err != nil {
				return resource.NonRetryableError(err)
			}
			return nil
		})
		if isResourceTimeoutError(err) {
			_, err = s3conn.PutBucketPolicy(params)
		}
		if err != nil {
			return fmt.Errorf("Error putting S3 policy: %s", err)
		}
	} else {
		log.Printf("[DEBUG] S3 bucket: %s, delete policy: %s", bucket, policy)
		_, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
			return s3conn.DeleteBucketPolicy(&s3.DeleteBucketPolicyInput{
				Bucket: aws.String(bucket),
			})
		})

		if err != nil {
			return fmt.Errorf("Error deleting S3 policy: %s", err)
		}
	}

	return nil
}

func resourceAwsS3BucketCorsUpdate(s3conn *s3.S3, d *schema.ResourceData) error {
	bucket := d.Get("bucket").(string)
	rawCors := d.Get("cors_rule").([]interface{})

	if len(rawCors) == 0 {
		// Delete CORS
		log.Printf("[DEBUG] S3 bucket: %s, delete CORS", bucket)

		_, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
			return s3conn.DeleteBucketCors(&s3.DeleteBucketCorsInput{
				Bucket: aws.String(bucket),
			})
		})
		if err != nil {
			return fmt.Errorf("Error deleting S3 CORS: %s", err)
		}
	} else {
		// Put CORS
		rules := make([]*s3.CORSRule, 0, len(rawCors))
		for _, cors := range rawCors {
			corsMap := cors.(map[string]interface{})
			r := &s3.CORSRule{}
			for k, v := range corsMap {
				log.Printf("[DEBUG] S3 bucket: %s, put CORS: %#v, %#v", bucket, k, v)
				if k == "max_age_seconds" {
					r.MaxAgeSeconds = aws.Int64(int64(v.(int)))
				} else {
					vMap := make([]*string, len(v.([]interface{})))
					for i, vv := range v.([]interface{}) {
						if str, ok := vv.(string); ok {
							vMap[i] = aws.String(str)
						}
					}
					switch k {
					case "allowed_headers":
						r.AllowedHeaders = vMap
					case "allowed_methods":
						r.AllowedMethods = vMap
					case "allowed_origins":
						r.AllowedOrigins = vMap
					case "expose_headers":
						r.ExposeHeaders = vMap
					}
				}
			}
			rules = append(rules, r)
		}
		corsInput := &s3.PutBucketCorsInput{
			Bucket: aws.String(bucket),
			CORSConfiguration: &s3.CORSConfiguration{
				CORSRules: rules,
			},
		}
		log.Printf("[DEBUG] S3 bucket: %s, put CORS: %#v", bucket, corsInput)

		_, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
			return s3conn.PutBucketCors(corsInput)
		})
		if err != nil {
			return fmt.Errorf("Error putting S3 CORS: %s", err)
		}
	}

	return nil
}

func resourceAwsS3BucketWebsiteUpdate(s3conn *s3.S3, d *schema.ResourceData) error {
	ws := d.Get("website").([]interface{})

	if len(ws) == 0 {
		return resourceAwsS3BucketWebsiteDelete(s3conn, d)
	}

	var w map[string]interface{}
	if ws[0] != nil {
		w = ws[0].(map[string]interface{})
	} else {
		w = make(map[string]interface{})
	}
	return resourceAwsS3BucketWebsitePut(s3conn, d, w)
}

func resourceAwsS3BucketWebsitePut(s3conn *s3.S3, d *schema.ResourceData, website map[string]interface{}) error {
	bucket := d.Get("bucket").(string)

	var indexDocument, errorDocument, redirectAllRequestsTo, routingRules string
	if v, ok := website["index_document"]; ok {
		indexDocument = v.(string)
	}
	if v, ok := website["error_document"]; ok {
		errorDocument = v.(string)
	}
	if v, ok := website["redirect_all_requests_to"]; ok {
		redirectAllRequestsTo = v.(string)
	}
	if v, ok := website["routing_rules"]; ok {
		routingRules = v.(string)
	}

	if indexDocument == "" && redirectAllRequestsTo == "" {
		return fmt.Errorf("Must specify either index_document or redirect_all_requests_to.")
	}

	websiteConfiguration := &s3.WebsiteConfiguration{}

	if indexDocument != "" {
		websiteConfiguration.IndexDocument = &s3.IndexDocument{Suffix: aws.String(indexDocument)}
	}

	if errorDocument != "" {
		websiteConfiguration.ErrorDocument = &s3.ErrorDocument{Key: aws.String(errorDocument)}
	}

	if redirectAllRequestsTo != "" {
		redirect, err := url.Parse(redirectAllRequestsTo)
		if err == nil && redirect.Scheme != "" {
			var redirectHostBuf bytes.Buffer
			redirectHostBuf.WriteString(redirect.Host)
			if redirect.Path != "" {
				redirectHostBuf.WriteString(redirect.Path)
			}
			if redirect.RawQuery != "" {
				redirectHostBuf.WriteString("?")
				redirectHostBuf.WriteString(redirect.RawQuery)
			}
			websiteConfiguration.RedirectAllRequestsTo = &s3.RedirectAllRequestsTo{HostName: aws.String(redirectHostBuf.String()), Protocol: aws.String(redirect.Scheme)}
		} else {
			websiteConfiguration.RedirectAllRequestsTo = &s3.RedirectAllRequestsTo{HostName: aws.String(redirectAllRequestsTo)}
		}
	}

	if routingRules != "" {
		var unmarshaledRules []*s3.RoutingRule
		if err := json.Unmarshal([]byte(routingRules), &unmarshaledRules); err != nil {
			return err
		}
		websiteConfiguration.RoutingRules = unmarshaledRules
	}

	putInput := &s3.PutBucketWebsiteInput{
		Bucket:               aws.String(bucket),
		WebsiteConfiguration: websiteConfiguration,
	}

	log.Printf("[DEBUG] S3 put bucket website: %#v", putInput)

	_, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.PutBucketWebsite(putInput)
	})
	if err != nil {
		return fmt.Errorf("Error putting S3 website: %s", err)
	}

	return nil
}

func resourceAwsS3BucketWebsiteDelete(s3conn *s3.S3, d *schema.ResourceData) error {
	bucket := d.Get("bucket").(string)
	deleteInput := &s3.DeleteBucketWebsiteInput{Bucket: aws.String(bucket)}

	log.Printf("[DEBUG] S3 delete bucket website: %#v", deleteInput)

	_, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.DeleteBucketWebsite(deleteInput)
	})
	if err != nil {
		return fmt.Errorf("Error deleting S3 website: %s", err)
	}

	d.Set("website_endpoint", "")
	d.Set("website_domain", "")

	return nil
}

func websiteEndpoint(s3conn *s3.S3, d *schema.ResourceData) (*S3Website, error) {
	// If the bucket doesn't have a website configuration, return an empty
	// endpoint
	if _, ok := d.GetOk("website"); !ok {
		return nil, nil
	}

	bucket := d.Get("bucket").(string)

	// Lookup the region for this bucket

	locationResponse, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.GetBucketLocation(
			&s3.GetBucketLocationInput{
				Bucket: aws.String(bucket),
			},
		)
	})
	if err != nil {
		return nil, err
	}
	location := locationResponse.(*s3.GetBucketLocationOutput)
	var region string
	if location.LocationConstraint != nil {
		region = *location.LocationConstraint
	}

	return WebsiteEndpoint(bucket, region), nil
}

func bucketDomainName(bucket string) string {
	return fmt.Sprintf("%s.s3.amazonaws.com", bucket)
}

// https://docs.aws.amazon.com/general/latest/gr/rande.html#s3_region
func BucketRegionalDomainName(bucket string, region string) (string, error) {
	// Return a default AWS Commercial domain name if no region is provided
	// Otherwise EndpointFor() will return BUCKET.s3..amazonaws.com
	if region == "" {
		return fmt.Sprintf("%s.s3.amazonaws.com", bucket), nil
	}
	endpoint, err := endpoints.DefaultResolver().EndpointFor(endpoints.S3ServiceID, region)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s.%s", bucket, strings.TrimPrefix(endpoint.URL, "https://")), nil
}

func WebsiteEndpoint(bucket string, region string) *S3Website {
	domain := WebsiteDomainUrl(region)
	return &S3Website{Endpoint: fmt.Sprintf("%s.%s", bucket, domain), Domain: domain}
}

func WebsiteDomainUrl(region string) string {
	region = normalizeRegion(region)

	// Different regions have different syntax for website endpoints
	// https://docs.aws.amazon.com/AmazonS3/latest/dev/WebsiteEndpoints.html
	// https://docs.aws.amazon.com/general/latest/gr/rande.html#s3_website_region_endpoints
	if isOldRegion(region) {
		return fmt.Sprintf("s3-website-%s.amazonaws.com", region)
	}
	if partition, ok := endpoints.PartitionForRegion(endpoints.DefaultPartitions(), region); ok && partition.ID() == endpoints.AwsCnPartitionID {
		return fmt.Sprintf("s3-website.%s.amazonaws.com.cn", region)
	}
	return fmt.Sprintf("s3-website.%s.amazonaws.com", region)
}

func isOldRegion(region string) bool {
	oldRegions := []string{
		"ap-northeast-1",
		"ap-southeast-1",
		"ap-southeast-2",
		"eu-west-1",
		"sa-east-1",
		"us-east-1",
		"us-gov-west-1",
		"us-west-1",
		"us-west-2",
	}
	for _, r := range oldRegions {
		if region == r {
			return true
		}
	}
	return false
}

func resourceAwsS3BucketAclUpdate(s3conn *s3.S3, d *schema.ResourceData) error {
	acl := d.Get("acl").(string)
	bucket := d.Get("bucket").(string)

	i := &s3.PutBucketAclInput{
		Bucket: aws.String(bucket),
		ACL:    aws.String(acl),
	}
	log.Printf("[DEBUG] S3 put bucket ACL: %#v", i)

	_, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.PutBucketAcl(i)
	})
	if err != nil {
		return fmt.Errorf("Error putting S3 ACL: %s", err)
	}

	return nil
}

func resourceAwsS3BucketVersioningUpdate(s3conn *s3.S3, d *schema.ResourceData) error {
	v := d.Get("versioning").([]interface{})
	bucket := d.Get("bucket").(string)
	vc := &s3.VersioningConfiguration{}

	if len(v) > 0 {
		c := v[0].(map[string]interface{})

		if c["enabled"].(bool) {
			vc.Status = aws.String(s3.BucketVersioningStatusEnabled)
		} else {
			vc.Status = aws.String(s3.BucketVersioningStatusSuspended)
		}

		if c["mfa_delete"].(bool) {
			vc.MFADelete = aws.String(s3.MFADeleteEnabled)
		} else {
			vc.MFADelete = aws.String(s3.MFADeleteDisabled)
		}

	} else {
		vc.Status = aws.String(s3.BucketVersioningStatusSuspended)
	}

	i := &s3.PutBucketVersioningInput{
		Bucket:                  aws.String(bucket),
		VersioningConfiguration: vc,
	}
	log.Printf("[DEBUG] S3 put bucket versioning: %#v", i)

	_, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.PutBucketVersioning(i)
	})
	if err != nil {
		return fmt.Errorf("Error putting S3 versioning: %s", err)
	}

	return nil
}

func resourceAwsS3BucketLoggingUpdate(s3conn *s3.S3, d *schema.ResourceData) error {
	logging := d.Get("logging").(*schema.Set).List()
	bucket := d.Get("bucket").(string)
	loggingStatus := &s3.BucketLoggingStatus{}

	if len(logging) > 0 {
		c := logging[0].(map[string]interface{})

		loggingEnabled := &s3.LoggingEnabled{}
		if val, ok := c["target_bucket"]; ok {
			loggingEnabled.TargetBucket = aws.String(val.(string))
		}
		if val, ok := c["target_prefix"]; ok {
			loggingEnabled.TargetPrefix = aws.String(val.(string))
		}

		loggingStatus.LoggingEnabled = loggingEnabled
	}

	i := &s3.PutBucketLoggingInput{
		Bucket:              aws.String(bucket),
		BucketLoggingStatus: loggingStatus,
	}
	log.Printf("[DEBUG] S3 put bucket logging: %#v", i)

	_, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.PutBucketLogging(i)
	})
	if err != nil {
		return fmt.Errorf("Error putting S3 logging: %s", err)
	}

	return nil
}

func resourceAwsS3BucketAccelerationUpdate(s3conn *s3.S3, d *schema.ResourceData) error {
	bucket := d.Get("bucket").(string)
	enableAcceleration := d.Get("acceleration_status").(string)

	i := &s3.PutBucketAccelerateConfigurationInput{
		Bucket: aws.String(bucket),
		AccelerateConfiguration: &s3.AccelerateConfiguration{
			Status: aws.String(enableAcceleration),
		},
	}
	log.Printf("[DEBUG] S3 put bucket acceleration: %#v", i)

	_, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.PutBucketAccelerateConfiguration(i)
	})
	if err != nil {
		return fmt.Errorf("Error putting S3 acceleration: %s", err)
	}

	return nil
}

func resourceAwsS3BucketRequestPayerUpdate(s3conn *s3.S3, d *schema.ResourceData) error {
	bucket := d.Get("bucket").(string)
	payer := d.Get("request_payer").(string)

	i := &s3.PutBucketRequestPaymentInput{
		Bucket: aws.String(bucket),
		RequestPaymentConfiguration: &s3.RequestPaymentConfiguration{
			Payer: aws.String(payer),
		},
	}
	log.Printf("[DEBUG] S3 put bucket request payer: %#v", i)

	_, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.PutBucketRequestPayment(i)
	})
	if err != nil {
		return fmt.Errorf("Error putting S3 request payer: %s", err)
	}

	return nil
}

func resourceAwsS3BucketServerSideEncryptionConfigurationUpdate(s3conn *s3.S3, d *schema.ResourceData) error {
	bucket := d.Get("bucket").(string)
	serverSideEncryptionConfiguration := d.Get("server_side_encryption_configuration").([]interface{})
	if len(serverSideEncryptionConfiguration) == 0 {
		log.Printf("[DEBUG] Delete server side encryption configuration: %#v", serverSideEncryptionConfiguration)
		i := &s3.DeleteBucketEncryptionInput{
			Bucket: aws.String(bucket),
		}

		_, err := s3conn.DeleteBucketEncryption(i)
		if err != nil {
			return fmt.Errorf("error removing S3 bucket server side encryption: %s", err)
		}
		return nil
	}

	c := serverSideEncryptionConfiguration[0].(map[string]interface{})

	rc := &s3.ServerSideEncryptionConfiguration{}

	rcRules := c["rule"].([]interface{})
	var rules []*s3.ServerSideEncryptionRule
	for _, v := range rcRules {
		rr := v.(map[string]interface{})
		rrDefault := rr["apply_server_side_encryption_by_default"].([]interface{})
		sseAlgorithm := rrDefault[0].(map[string]interface{})["sse_algorithm"].(string)
		kmsMasterKeyId := rrDefault[0].(map[string]interface{})["kms_master_key_id"].(string)
		rcDefaultRule := &s3.ServerSideEncryptionByDefault{
			SSEAlgorithm: aws.String(sseAlgorithm),
		}
		if kmsMasterKeyId != "" {
			rcDefaultRule.KMSMasterKeyID = aws.String(kmsMasterKeyId)
		}
		rcRule := &s3.ServerSideEncryptionRule{
			ApplyServerSideEncryptionByDefault: rcDefaultRule,
		}

		rules = append(rules, rcRule)
	}

	rc.Rules = rules
	i := &s3.PutBucketEncryptionInput{
		Bucket:                            aws.String(bucket),
		ServerSideEncryptionConfiguration: rc,
	}
	log.Printf("[DEBUG] S3 put bucket replication configuration: %#v", i)

	_, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.PutBucketEncryption(i)
	})
	if err != nil {
		return fmt.Errorf("error putting S3 server side encryption configuration: %s", err)
	}

	return nil
}

func resourceAwsS3BucketObjectLockConfigurationUpdate(s3conn *s3.S3, d *schema.ResourceData) error {
	// S3 Object Lock configuration cannot be deleted, only updated.
	req := &s3.PutObjectLockConfigurationInput{
		Bucket:                  aws.String(d.Get("bucket").(string)),
		ObjectLockConfiguration: expandS3ObjectLockConfiguration(d.Get("object_lock_configuration").([]interface{})),
	}

	_, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.PutObjectLockConfiguration(req)
	})
	if err != nil {
		return fmt.Errorf("error putting S3 object lock configuration: %s", err)
	}

	return nil
}

func resourceAwsS3BucketReplicationConfigurationUpdate(s3conn *s3.S3, d *schema.ResourceData) error {
	bucket := d.Get("bucket").(string)
	replicationConfiguration := d.Get("replication_configuration").([]interface{})

	if len(replicationConfiguration) == 0 {
		i := &s3.DeleteBucketReplicationInput{
			Bucket: aws.String(bucket),
		}

		_, err := s3conn.DeleteBucketReplication(i)
		if err != nil {
			return fmt.Errorf("Error removing S3 bucket replication: %s", err)
		}
		return nil
	}

	hasVersioning := false
	// Validate that bucket versioning is enabled
	if versioning, ok := d.GetOk("versioning"); ok {
		v := versioning.([]interface{})

		if v[0].(map[string]interface{})["enabled"].(bool) {
			hasVersioning = true
		}
	}

	if !hasVersioning {
		return fmt.Errorf("versioning must be enabled to allow S3 bucket replication")
	}

	c := replicationConfiguration[0].(map[string]interface{})

	rc := &s3.ReplicationConfiguration{}
	if val, ok := c["role"]; ok {
		rc.Role = aws.String(val.(string))
	}

	rcRules := c["rules"].(*schema.Set).List()
	rules := []*s3.ReplicationRule{}
	for _, v := range rcRules {
		rr := v.(map[string]interface{})
		rcRule := &s3.ReplicationRule{}
		if status, ok := rr["status"]; ok && status != "" {
			rcRule.Status = aws.String(status.(string))
		} else {
			continue
		}

		if rrid, ok := rr["id"]; ok && rrid != "" {
			rcRule.ID = aws.String(rrid.(string))
		}

		ruleDestination := &s3.Destination{}
		if dest, ok := rr["destination"].(*schema.Set); ok && dest.Len() > 0 {
			bd := dest.List()[0].(map[string]interface{})
			ruleDestination.Bucket = aws.String(bd["bucket"].(string))

			if storageClass, ok := bd["storage_class"]; ok && storageClass != "" {
				ruleDestination.StorageClass = aws.String(storageClass.(string))
			}

			if replicaKmsKeyId, ok := bd["replica_kms_key_id"]; ok && replicaKmsKeyId != "" {
				ruleDestination.EncryptionConfiguration = &s3.EncryptionConfiguration{
					ReplicaKmsKeyID: aws.String(replicaKmsKeyId.(string)),
				}
			}

			if account, ok := bd["account_id"]; ok && account != "" {
				ruleDestination.Account = aws.String(account.(string))
			}

			if aclTranslation, ok := bd["access_control_translation"].([]interface{}); ok && len(aclTranslation) > 0 {
				aclTranslationValues := aclTranslation[0].(map[string]interface{})
				ruleAclTranslation := &s3.AccessControlTranslation{}
				ruleAclTranslation.Owner = aws.String(aclTranslationValues["owner"].(string))
				ruleDestination.AccessControlTranslation = ruleAclTranslation
			}

		}
		rcRule.Destination = ruleDestination

		if ssc, ok := rr["source_selection_criteria"].(*schema.Set); ok && ssc.Len() > 0 {
			sscValues := ssc.List()[0].(map[string]interface{})
			ruleSsc := &s3.SourceSelectionCriteria{}
			if sseKms, ok := sscValues["sse_kms_encrypted_objects"].(*schema.Set); ok && sseKms.Len() > 0 {
				sseKmsValues := sseKms.List()[0].(map[string]interface{})
				sseKmsEncryptedObjects := &s3.SseKmsEncryptedObjects{}
				if sseKmsValues["enabled"].(bool) {
					sseKmsEncryptedObjects.Status = aws.String(s3.SseKmsEncryptedObjectsStatusEnabled)
				} else {
					sseKmsEncryptedObjects.Status = aws.String(s3.SseKmsEncryptedObjectsStatusDisabled)
				}
				ruleSsc.SseKmsEncryptedObjects = sseKmsEncryptedObjects
			}
			rcRule.SourceSelectionCriteria = ruleSsc
		}

		if f, ok := rr["filter"].([]interface{}); ok && len(f) > 0 && f[0] != nil {
			// XML schema V2.
			rcRule.Priority = aws.Int64(int64(rr["priority"].(int)))
			rcRule.Filter = &s3.ReplicationRuleFilter{}
			filter := f[0].(map[string]interface{})
			tags := filter["tags"].(map[string]interface{})
			if len(tags) > 0 {
				rcRule.Filter.And = &s3.ReplicationRuleAndOperator{
					Prefix: aws.String(filter["prefix"].(string)),
					Tags:   tagsFromMapS3(tags),
				}
			} else {
				rcRule.Filter.Prefix = aws.String(filter["prefix"].(string))
			}
			rcRule.DeleteMarkerReplication = &s3.DeleteMarkerReplication{
				Status: aws.String(s3.DeleteMarkerReplicationStatusDisabled),
			}
		} else {
			// XML schema V1.
			rcRule.Prefix = aws.String(rr["prefix"].(string))
		}

		rules = append(rules, rcRule)
	}

	rc.Rules = rules
	i := &s3.PutBucketReplicationInput{
		Bucket:                   aws.String(bucket),
		ReplicationConfiguration: rc,
	}
	log.Printf("[DEBUG] S3 put bucket replication configuration: %#v", i)

	err := resource.Retry(1*time.Minute, func() *resource.RetryError {
		_, err := s3conn.PutBucketReplication(i)
		if isAWSErr(err, s3.ErrCodeNoSuchBucket, "") || isAWSErr(err, "InvalidRequest", "Versioning must be 'Enabled' on the bucket") {
			return resource.RetryableError(err)
		}
		if err != nil {
			return resource.NonRetryableError(err)
		}
		return nil
	})
	if isResourceTimeoutError(err) {
		_, err = s3conn.PutBucketReplication(i)
	}
	if err != nil {
		return fmt.Errorf("Error putting S3 replication configuration: %s", err)
	}

	return nil
}

func resourceAwsS3BucketLifecycleUpdate(s3conn *s3.S3, d *schema.ResourceData) error {
	bucket := d.Get("bucket").(string)

	lifecycleRules := d.Get("lifecycle_rule").([]interface{})

	if len(lifecycleRules) == 0 {
		i := &s3.DeleteBucketLifecycleInput{
			Bucket: aws.String(bucket),
		}

		_, err := s3conn.DeleteBucketLifecycle(i)
		if err != nil {
			return fmt.Errorf("Error removing S3 lifecycle: %s", err)
		}
		return nil
	}

	rules := make([]*s3.LifecycleRule, 0, len(lifecycleRules))

	for i, lifecycleRule := range lifecycleRules {
		r := lifecycleRule.(map[string]interface{})

		rule := &s3.LifecycleRule{}

		// Filter
		tags := r["tags"].(map[string]interface{})
		filter := &s3.LifecycleRuleFilter{}
		if len(tags) > 0 {
			lifecycleRuleAndOp := &s3.LifecycleRuleAndOperator{}
			lifecycleRuleAndOp.SetPrefix(r["prefix"].(string))
			lifecycleRuleAndOp.SetTags(tagsFromMapS3(tags))
			filter.SetAnd(lifecycleRuleAndOp)
		} else {
			filter.SetPrefix(r["prefix"].(string))
		}
		rule.SetFilter(filter)

		// ID
		if val, ok := r["id"].(string); ok && val != "" {
			rule.ID = aws.String(val)
		} else {
			rule.ID = aws.String(resource.PrefixedUniqueId("tf-s3-lifecycle-"))
		}

		// Enabled
		if val, ok := r["enabled"].(bool); ok && val {
			rule.Status = aws.String(s3.ExpirationStatusEnabled)
		} else {
			rule.Status = aws.String(s3.ExpirationStatusDisabled)
		}

		// AbortIncompleteMultipartUpload
		if val, ok := r["abort_incomplete_multipart_upload_days"].(int); ok && val > 0 {
			rule.AbortIncompleteMultipartUpload = &s3.AbortIncompleteMultipartUpload{
				DaysAfterInitiation: aws.Int64(int64(val)),
			}
		}

		// Expiration
		expiration := d.Get(fmt.Sprintf("lifecycle_rule.%d.expiration", i)).(*schema.Set).List()
		if len(expiration) > 0 {
			e := expiration[0].(map[string]interface{})
			i := &s3.LifecycleExpiration{}

			if val, ok := e["date"].(string); ok && val != "" {
				t, err := time.Parse(time.RFC3339, fmt.Sprintf("%sT00:00:00Z", val))
				if err != nil {
					return fmt.Errorf("Error Parsing AWS S3 Bucket Lifecycle Expiration Date: %s", err.Error())
				}
				i.Date = aws.Time(t)
			} else if val, ok := e["days"].(int); ok && val > 0 {
				i.Days = aws.Int64(int64(val))
			} else if val, ok := e["expired_object_delete_marker"].(bool); ok {
				i.ExpiredObjectDeleteMarker = aws.Bool(val)
			}
			rule.Expiration = i
		}

		// NoncurrentVersionExpiration
		nc_expiration := d.Get(fmt.Sprintf("lifecycle_rule.%d.noncurrent_version_expiration", i)).(*schema.Set).List()
		if len(nc_expiration) > 0 {
			e := nc_expiration[0].(map[string]interface{})

			if val, ok := e["days"].(int); ok && val > 0 {
				rule.NoncurrentVersionExpiration = &s3.NoncurrentVersionExpiration{
					NoncurrentDays: aws.Int64(int64(val)),
				}
			}
		}

		// Transitions
		transitions := d.Get(fmt.Sprintf("lifecycle_rule.%d.transition", i)).(*schema.Set).List()
		if len(transitions) > 0 {
			rule.Transitions = make([]*s3.Transition, 0, len(transitions))
			for _, transition := range transitions {
				transition := transition.(map[string]interface{})
				i := &s3.Transition{}
				if val, ok := transition["date"].(string); ok && val != "" {
					t, err := time.Parse(time.RFC3339, fmt.Sprintf("%sT00:00:00Z", val))
					if err != nil {
						return fmt.Errorf("Error Parsing AWS S3 Bucket Lifecycle Expiration Date: %s", err.Error())
					}
					i.Date = aws.Time(t)
				} else if val, ok := transition["days"].(int); ok && val >= 0 {
					i.Days = aws.Int64(int64(val))
				}
				if val, ok := transition["storage_class"].(string); ok && val != "" {
					i.StorageClass = aws.String(val)
				}

				rule.Transitions = append(rule.Transitions, i)
			}
		}
		// NoncurrentVersionTransitions
		nc_transitions := d.Get(fmt.Sprintf("lifecycle_rule.%d.noncurrent_version_transition", i)).(*schema.Set).List()
		if len(nc_transitions) > 0 {
			rule.NoncurrentVersionTransitions = make([]*s3.NoncurrentVersionTransition, 0, len(nc_transitions))
			for _, transition := range nc_transitions {
				transition := transition.(map[string]interface{})
				i := &s3.NoncurrentVersionTransition{}
				if val, ok := transition["days"].(int); ok && val >= 0 {
					i.NoncurrentDays = aws.Int64(int64(val))
				}
				if val, ok := transition["storage_class"].(string); ok && val != "" {
					i.StorageClass = aws.String(val)
				}

				rule.NoncurrentVersionTransitions = append(rule.NoncurrentVersionTransitions, i)
			}
		}

		rules = append(rules, rule)
	}

	i := &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String(bucket),
		LifecycleConfiguration: &s3.BucketLifecycleConfiguration{
			Rules: rules,
		},
	}

	_, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return s3conn.PutBucketLifecycleConfiguration(i)
	})
	if err != nil {
		return fmt.Errorf("Error putting S3 lifecycle: %s", err)
	}

	return nil
}

func flattenAwsS3ServerSideEncryptionConfiguration(c *s3.ServerSideEncryptionConfiguration) []map[string]interface{} {
	var encryptionConfiguration []map[string]interface{}
	rules := make([]interface{}, 0, len(c.Rules))
	for _, v := range c.Rules {
		if v.ApplyServerSideEncryptionByDefault != nil {
			r := make(map[string]interface{})
			d := make(map[string]interface{})
			d["kms_master_key_id"] = aws.StringValue(v.ApplyServerSideEncryptionByDefault.KMSMasterKeyID)
			d["sse_algorithm"] = aws.StringValue(v.ApplyServerSideEncryptionByDefault.SSEAlgorithm)
			r["apply_server_side_encryption_by_default"] = []map[string]interface{}{d}
			rules = append(rules, r)
		}
	}
	encryptionConfiguration = append(encryptionConfiguration, map[string]interface{}{
		"rule": rules,
	})
	return encryptionConfiguration
}

func flattenAwsS3BucketReplicationConfiguration(r *s3.ReplicationConfiguration) []map[string]interface{} {
	replication_configuration := make([]map[string]interface{}, 0, 1)

	if r == nil {
		return replication_configuration
	}

	m := make(map[string]interface{})

	if r.Role != nil && *r.Role != "" {
		m["role"] = *r.Role
	}

	rules := make([]interface{}, 0, len(r.Rules))
	for _, v := range r.Rules {
		t := make(map[string]interface{})
		if v.Destination != nil {
			rd := make(map[string]interface{})
			if v.Destination.Bucket != nil {
				rd["bucket"] = *v.Destination.Bucket
			}
			if v.Destination.StorageClass != nil {
				rd["storage_class"] = *v.Destination.StorageClass
			}
			if v.Destination.EncryptionConfiguration != nil {
				if v.Destination.EncryptionConfiguration.ReplicaKmsKeyID != nil {
					rd["replica_kms_key_id"] = *v.Destination.EncryptionConfiguration.ReplicaKmsKeyID
				}
			}
			if v.Destination.Account != nil {
				rd["account_id"] = *v.Destination.Account
			}
			if v.Destination.AccessControlTranslation != nil {
				rdt := map[string]interface{}{
					"owner": aws.StringValue(v.Destination.AccessControlTranslation.Owner),
				}
				rd["access_control_translation"] = []interface{}{rdt}
			}
			t["destination"] = schema.NewSet(destinationHash, []interface{}{rd})
		}

		if v.ID != nil {
			t["id"] = *v.ID
		}
		if v.Prefix != nil {
			t["prefix"] = *v.Prefix
		}
		if v.Status != nil {
			t["status"] = *v.Status
		}
		if vssc := v.SourceSelectionCriteria; vssc != nil {
			tssc := make(map[string]interface{})
			if vssc.SseKmsEncryptedObjects != nil {
				tSseKms := make(map[string]interface{})
				if *vssc.SseKmsEncryptedObjects.Status == s3.SseKmsEncryptedObjectsStatusEnabled {
					tSseKms["enabled"] = true
				} else if *vssc.SseKmsEncryptedObjects.Status == s3.SseKmsEncryptedObjectsStatusDisabled {
					tSseKms["enabled"] = false
				}
				tssc["sse_kms_encrypted_objects"] = schema.NewSet(sourceSseKmsObjectsHash, []interface{}{tSseKms})
			}
			t["source_selection_criteria"] = schema.NewSet(sourceSelectionCriteriaHash, []interface{}{tssc})
		}

		if v.Priority != nil {
			t["priority"] = int(aws.Int64Value(v.Priority))
		}

		if f := v.Filter; f != nil {
			m := map[string]interface{}{}
			if f.Prefix != nil {
				m["prefix"] = aws.StringValue(f.Prefix)
			}
			if t := f.Tag; t != nil {
				m["tags"] = tagsMapToRaw(tagsToMapS3([]*s3.Tag{t}))
			}
			if a := f.And; a != nil {
				m["prefix"] = aws.StringValue(a.Prefix)
				m["tags"] = tagsMapToRaw(tagsToMapS3(a.Tags))
			}
			t["filter"] = []interface{}{m}
		}

		rules = append(rules, t)
	}
	m["rules"] = schema.NewSet(rulesHash, rules)

	replication_configuration = append(replication_configuration, m)

	return replication_configuration
}

func normalizeRoutingRules(w []*s3.RoutingRule) (string, error) {
	withNulls, err := json.Marshal(w)
	if err != nil {
		return "", err
	}

	var rules []map[string]interface{}
	if err := json.Unmarshal(withNulls, &rules); err != nil {
		return "", err
	}

	var cleanRules []map[string]interface{}
	for _, rule := range rules {
		cleanRules = append(cleanRules, removeNil(rule))
	}

	withoutNulls, err := json.Marshal(cleanRules)
	if err != nil {
		return "", err
	}

	return string(withoutNulls), nil
}

func removeNil(data map[string]interface{}) map[string]interface{} {
	withoutNil := make(map[string]interface{})

	for k, v := range data {
		if v == nil {
			continue
		}

		switch v := v.(type) {
		case map[string]interface{}:
			withoutNil[k] = removeNil(v)
		default:
			withoutNil[k] = v
		}
	}

	return withoutNil
}

func normalizeRegion(region string) string {
	// Default to us-east-1 if the bucket doesn't have a region:
	// http://docs.aws.amazon.com/AmazonS3/latest/API/RESTBucketGETlocation.html
	if region == "" {
		region = "us-east-1"
	}

	return region
}

// validateS3BucketName validates any S3 bucket name that is not inside the us-east-1 region.
// Buckets outside of this region have to be DNS-compliant. After the same restrictions are
// applied to buckets in the us-east-1 region, this function can be refactored as a SchemaValidateFunc
func validateS3BucketName(value string, region string) error {
	if region != "us-east-1" {
		if (len(value) < 3) || (len(value) > 63) {
			return fmt.Errorf("%q must contain from 3 to 63 characters", value)
		}
		if !regexp.MustCompile(`^[0-9a-z-.]+$`).MatchString(value) {
			return fmt.Errorf("only lowercase alphanumeric characters and hyphens allowed in %q", value)
		}
		if regexp.MustCompile(`^(?:[0-9]{1,3}\.){3}[0-9]{1,3}$`).MatchString(value) {
			return fmt.Errorf("%q must not be formatted as an IP address", value)
		}
		if strings.HasPrefix(value, `.`) {
			return fmt.Errorf("%q cannot start with a period", value)
		}
		if strings.HasSuffix(value, `.`) {
			return fmt.Errorf("%q cannot end with a period", value)
		}
		if strings.Contains(value, `..`) {
			return fmt.Errorf("%q can be only one period between labels", value)
		}
	} else {
		if len(value) > 255 {
			return fmt.Errorf("%q must contain less than 256 characters", value)
		}
		if !regexp.MustCompile(`^[0-9a-zA-Z-._]+$`).MatchString(value) {
			return fmt.Errorf("only alphanumeric characters, hyphens, periods, and underscores allowed in %q", value)
		}
	}
	return nil
}

func expirationHash(v interface{}) int {
	var buf bytes.Buffer
	m := v.(map[string]interface{})
	if v, ok := m["date"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}
	if v, ok := m["days"]; ok {
		buf.WriteString(fmt.Sprintf("%d-", v.(int)))
	}
	if v, ok := m["expired_object_delete_marker"]; ok {
		buf.WriteString(fmt.Sprintf("%t-", v.(bool)))
	}
	return hashcode.String(buf.String())
}

func transitionHash(v interface{}) int {
	var buf bytes.Buffer
	m := v.(map[string]interface{})
	if v, ok := m["date"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}
	if v, ok := m["days"]; ok {
		buf.WriteString(fmt.Sprintf("%d-", v.(int)))
	}
	if v, ok := m["storage_class"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}
	return hashcode.String(buf.String())
}

func rulesHash(v interface{}) int {
	var buf bytes.Buffer
	m := v.(map[string]interface{})

	if v, ok := m["id"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}
	if v, ok := m["prefix"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}
	if v, ok := m["status"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}
	if v, ok := m["destination"].(*schema.Set); ok && v.Len() > 0 {
		buf.WriteString(fmt.Sprintf("%d-", destinationHash(v.List()[0])))
	}
	if v, ok := m["source_selection_criteria"].(*schema.Set); ok && v.Len() > 0 && v.List()[0] != nil {
		buf.WriteString(fmt.Sprintf("%d-", sourceSelectionCriteriaHash(v.List()[0])))
	}
	if v, ok := m["priority"]; ok {
		buf.WriteString(fmt.Sprintf("%d-", v.(int)))
	}
	if v, ok := m["filter"].([]interface{}); ok && len(v) > 0 && v[0] != nil {
		buf.WriteString(fmt.Sprintf("%d-", replicationRuleFilterHash(v[0])))
	}
	return hashcode.String(buf.String())
}

func replicationRuleFilterHash(v interface{}) int {
	var buf bytes.Buffer
	m := v.(map[string]interface{})
	if v, ok := m["prefix"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}
	if v, ok := m["tags"]; ok {
		buf.WriteString(fmt.Sprintf("%d-", tagsMapToHash(v.(map[string]interface{}))))
	}
	return hashcode.String(buf.String())
}

func destinationHash(v interface{}) int {
	var buf bytes.Buffer
	m := v.(map[string]interface{})

	if v, ok := m["bucket"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}
	if v, ok := m["storage_class"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}
	if v, ok := m["replica_kms_key_id"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}
	if v, ok := m["account"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}
	if v, ok := m["access_control_translation"].([]interface{}); ok && len(v) > 0 && v[0] != nil {
		buf.WriteString(fmt.Sprintf("%d-", accessControlTranslationHash(v[0])))
	}
	return hashcode.String(buf.String())
}

func accessControlTranslationHash(v interface{}) int {
	// v is nil if empty access_control_translation is given.
	if v == nil {
		return 0
	}
	var buf bytes.Buffer
	m := v.(map[string]interface{})

	if v, ok := m["owner"]; ok {
		buf.WriteString(fmt.Sprintf("%s-", v.(string)))
	}
	return hashcode.String(buf.String())
}

func sourceSelectionCriteriaHash(v interface{}) int {
	// v is nil if empty source_selection_criteria is given.
	if v == nil {
		return 0
	}
	var buf bytes.Buffer
	m := v.(map[string]interface{})

	if v, ok := m["sse_kms_encrypted_objects"].(*schema.Set); ok && v.Len() > 0 {
		buf.WriteString(fmt.Sprintf("%d-", sourceSseKmsObjectsHash(v.List()[0])))
	}
	return hashcode.String(buf.String())
}

func sourceSseKmsObjectsHash(v interface{}) int {
	var buf bytes.Buffer
	m := v.(map[string]interface{})

	if v, ok := m["enabled"]; ok {
		buf.WriteString(fmt.Sprintf("%t-", v.(bool)))
	}
	return hashcode.String(buf.String())
}

type S3Website struct {
	Endpoint, Domain string
}

//
// S3 Object Lock functions.
//

func readS3ObjectLockConfiguration(conn *s3.S3, bucket string) (interface{}, error) {
	resp, err := retryOnAwsCode(s3.ErrCodeNoSuchBucket, func() (interface{}, error) {
		return conn.GetObjectLockConfiguration(&s3.GetObjectLockConfigurationInput{
			Bucket: aws.String(bucket),
		})
	})
	if err != nil {
		if isAWSErr(err, "ObjectLockConfigurationNotFoundError", "") {
			return nil, nil
		}
		return nil, err
	}

	return flattenS3ObjectLockConfiguration(resp.(*s3.GetObjectLockConfigurationOutput).ObjectLockConfiguration), nil
}

func expandS3ObjectLockConfiguration(vConf []interface{}) *s3.ObjectLockConfiguration {
	if len(vConf) == 0 || vConf[0] == nil {
		return nil
	}

	mConf := vConf[0].(map[string]interface{})

	conf := &s3.ObjectLockConfiguration{}

	if vObjectLockEnabled, ok := mConf["object_lock_enabled"].(string); ok && vObjectLockEnabled != "" {
		conf.ObjectLockEnabled = aws.String(vObjectLockEnabled)
	}

	if vRule, ok := mConf["rule"].([]interface{}); ok && len(vRule) > 0 {
		mRule := vRule[0].(map[string]interface{})

		if vDefaultRetention, ok := mRule["default_retention"].([]interface{}); ok && len(vDefaultRetention) > 0 && vDefaultRetention[0] != nil {
			mDefaultRetention := vDefaultRetention[0].(map[string]interface{})

			conf.Rule = &s3.ObjectLockRule{
				DefaultRetention: &s3.DefaultRetention{},
			}

			if vMode, ok := mDefaultRetention["mode"].(string); ok && vMode != "" {
				conf.Rule.DefaultRetention.Mode = aws.String(vMode)
			}
			if vDays, ok := mDefaultRetention["days"].(int); ok && vDays > 0 {
				conf.Rule.DefaultRetention.Days = aws.Int64(int64(vDays))
			}
			if vYears, ok := mDefaultRetention["years"].(int); ok && vYears > 0 {
				conf.Rule.DefaultRetention.Years = aws.Int64(int64(vYears))
			}
		}
	}

	return conf
}

func flattenS3ObjectLockConfiguration(conf *s3.ObjectLockConfiguration) []interface{} {
	if conf == nil {
		return []interface{}{}
	}

	mConf := map[string]interface{}{
		"object_lock_enabled": aws.StringValue(conf.ObjectLockEnabled),
	}

	if conf.Rule != nil && conf.Rule.DefaultRetention != nil {
		mRule := map[string]interface{}{
			"default_retention": []interface{}{
				map[string]interface{}{
					"mode":  aws.StringValue(conf.Rule.DefaultRetention.Mode),
					"days":  int(aws.Int64Value(conf.Rule.DefaultRetention.Days)),
					"years": int(aws.Int64Value(conf.Rule.DefaultRetention.Years)),
				},
			},
		}

		mConf["rule"] = []interface{}{mRule}
	}

	return []interface{}{mConf}
}
