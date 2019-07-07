import { Component, OnInit, OnDestroy, Input } from '@angular/core';
import { FormControl, FormGroup, Validators } from '@angular/forms';
import { ISubscription } from 'rxjs/Subscription';
import { ApiService } from '../../../../../services/api.service';
import { Subject } from 'rxjs/Subject';
import 'rxjs/add/operator/switchMap';
import { Observable } from 'rxjs/Observable';
import { MatDialog, MatDialogConfig } from '@angular/material';
import { SeedWordDialogComponent } from '../../../../layout/seed-word-dialog/seed-word-dialog.component';
import { ConfirmationData } from '../../../../../app.datatypes';
import { showConfirmationModal } from '../../../../../utils';
import { MsgBarService } from '../../../../../services/msg-bar.service';

export class WalletFormData {
  creatingNewWallet: boolean;
  label: string;
  seed: string;
  password: string;
  useNormalSeed: boolean;
  lastSeed: string;
  lastCustomSeed: string;
  numberOfWords: number;
}

@Component({
  selector: 'app-create-wallet-form',
  templateUrl: './create-wallet-form.component.html',
  styleUrls: ['./create-wallet-form.component.scss'],
})
export class CreateWalletFormComponent implements OnInit, OnDestroy {
  @Input() create: boolean;
  @Input() onboarding: boolean;

  form: FormGroup;
  customSeedIsNormal = true;
  customSeedAccepted = false;
  encrypt = true;
  useNormalSeed = true;
  normalSeedConfirmed = false;
  lastSeed = '';
  numberOfWords = 0;

  private seed: Subject<string> = new Subject<string>();
  private statusSubscription: ISubscription;
  private seedValiditySubscription: ISubscription;

  private partialSeed: string[];

  constructor(
    private apiService: ApiService,
    private dialog: MatDialog,
    private msgBarService: MsgBarService,
  ) { }

  ngOnInit() {
    if (!this.onboarding) {
      this.initForm();
    } else {
      this.initForm(false, null);
    }
  }

  ngOnDestroy() {
    this.msgBarService.hide();
    this.statusSubscription.unsubscribe();
    this.seedValiditySubscription.unsubscribe();
  }

  get isValid(): boolean {
    return this.form.valid &&
      (
        (!this.useNormalSeed && (this.customSeedIsNormal || this.customSeedAccepted)) ||
        (this.create && this.useNormalSeed && this.normalSeedConfirmed) ||
        (!this.create && this.useNormalSeed && this.lastSeed.length > 2)
      );
  }

  onCustomSeedAcceptance(event) {
    this.customSeedAccepted = event.checked;
  }

  setEncrypt(event) {
    this.encrypt = event.checked;
    this.form.updateValueAndValidity();
  }

  getData(): WalletFormData {
    return {
      creatingNewWallet: this.create,
      label: this.form.value.label,
      seed: this.useNormalSeed ? this.lastSeed : this.form.value.seed,
      password: !this.onboarding && this.encrypt ? this.form.value.password : null,
      useNormalSeed: this.useNormalSeed,
      lastSeed: this.lastSeed,
      lastCustomSeed: this.form.value.seed,
      numberOfWords: !this.create ? this.form.value.number_of_words : this.numberOfWords,
    };
  }

  changeSeedType() {
    this.msgBarService.hide();

    if (!this.useNormalSeed) {
      this.useNormalSeed = true;
      this.removeConfirmations();
    } else {
      const confirmationData: ConfirmationData = {
        text: this.create ? 'wallet.new.seed.custom-seed-warning-text' : 'wallet.new.seed.custom-seed-warning-text-recovering',
        headerText: 'wallet.new.seed.custom-seed-warning-title',
        checkboxText: this.create ? 'wallet.new.seed.custom-seed-warning-check' : null,
        confirmButtonText: 'wallet.new.seed.custom-seed-warning-continue',
        cancelButtonText: 'wallet.new.seed.custom-seed-warning-cancel',
      };

      showConfirmationModal(this.dialog, confirmationData).afterClosed().subscribe(confirmationResult => {
        if (confirmationResult) {
          this.useNormalSeed = false;
          this.removeConfirmations();
        }
      });
    }
  }

  enterSeed() {
    if (!this.create) {
      this.partialSeed = [];
      this.askForWord(0);
      this.msgBarService.hide();
    }
  }

  confirmNormalSeed() {
    if (!this.normalSeedConfirmed) {
      this.partialSeed = [];
      this.askForWord(0);
      this.msgBarService.hide();
    }
  }

  private askForWord(wordIndex: number) {
    this.dialog.open(SeedWordDialogComponent, <MatDialogConfig> {
      width: '350px',
      data: {
        isForHwWallet: false,
        wordNumber: wordIndex + 1,
        restoringSoftwareWallet: !this.create,
      },
    }).afterClosed().subscribe(word => {
      if (word) {
        if (this.create) {
          const lastSeedWords = this.lastSeed.split(' ');
          if (word !== lastSeedWords[wordIndex]) {
            this.msgBarService.showError('wallet.new.seed.incorrect-word');

            return;
          }
        }

        this.partialSeed[wordIndex] = word;
        wordIndex += 1;

        if ((this.create && wordIndex < this.numberOfWords) || (!this.create && wordIndex < this.form.controls['number_of_words'].value)) {
          this.askForWord(wordIndex);
        } else {
          let enteredSeed = '';
          this.partialSeed.forEach(currentWord => enteredSeed += currentWord + ' ');
          enteredSeed = enteredSeed.substr(0, enteredSeed.length - 1);

          if (this.create) {
            this.normalSeedConfirmed = true;
          } else {
            this.apiService.post('wallet/seed/verify', {seed: enteredSeed}, {}, true)
              .subscribe(() => this.lastSeed = enteredSeed, () => this.msgBarService.showError('wallet.new.seed.invalid-seed'));
          }
        }
      }
    });
  }

  initForm(create: boolean = null, data: WalletFormData = null) {
    this.msgBarService.hide();

    create = create !== null ? create : this.create;

    this.lastSeed = '';
    this.useNormalSeed = true;

    const validators = [];
    if (create) {
      validators.push(this.seedMatchValidator.bind(this));
    }
    if (!this.onboarding) {
      validators.push(this.validatePasswords.bind(this));
    }
    validators.push(this.mustHaveSeed.bind(this));

    this.form = new FormGroup({}, validators);
    this.form.addControl('label', new FormControl(data ? data.label : '', [Validators.required]));
    this.form.addControl('seed', new FormControl(data ? data.lastCustomSeed : ''));
    this.form.addControl('confirm_seed', new FormControl(data ? data.lastCustomSeed : ''));
    this.form.addControl('password', new FormControl());
    this.form.addControl('confirm_password', new FormControl());
    this.form.addControl('number_of_words', new FormControl(data && data.numberOfWords ? data.numberOfWords : 12));

    this.removeConfirmations(false);

    if (create && !data) {
      this.generateSeed(128);
    }

    if (data) {
      setTimeout(() => { this.seed.next(data['seed']); });
      this.customSeedAccepted = true;
      this.useNormalSeed = data.useNormalSeed;
      this.lastSeed = data.lastSeed;
      this.normalSeedConfirmed = true;

      if (this.create) {
        this.numberOfWords = data.numberOfWords;
      }
    }

    if (this.statusSubscription && !this.statusSubscription.closed) {
      this.statusSubscription.unsubscribe();
    }
    this.statusSubscription = this.form.statusChanges.subscribe(() => {
      this.customSeedAccepted = false;
      this.seed.next(this.form.get('seed').value);
    });

    this.subscribeToSeedValidation();
  }

  generateSeed(entropy: number) {
    if (entropy === 128) {
      this.numberOfWords = 12;
    } else {
      this.numberOfWords = 24;
    }

    this.apiService.generateSeed(entropy).subscribe(seed => {
      this.lastSeed = seed;
      this.form.get('seed').setValue(seed);
      this.removeConfirmations();
    });
  }

  private removeConfirmations(cleanSecondSeedField = true) {
    this.customSeedAccepted = false;
    this.normalSeedConfirmed = false;
    if (cleanSecondSeedField) {
      this.form.get('confirm_seed').setValue('');
    }
    this.form.updateValueAndValidity();
  }

  private subscribeToSeedValidation() {
    if (this.seedValiditySubscription) {
      this.seedValiditySubscription.unsubscribe();
    }

    this.seedValiditySubscription = this.seed.asObservable().switchMap(seed => {
      if ((!this.seedMatchValidator() || !this.create) && !this.useNormalSeed) {
        return this.apiService.post('wallet/seed/verify', {seed}, {}, true);
      } else {
        return Observable.of(0);
      }
    }).subscribe(response => {
      if (response !== -1) {
        this.customSeedIsNormal = true;
      }
    }, error => {
      if (error.status && error.status === 422) {
        this.customSeedIsNormal = false;
      } else {
        this.customSeedIsNormal = true;
      }
      this.subscribeToSeedValidation();
    });
  }

  private validatePasswords() {
    if (this.encrypt && this.form && this.form.get('password') && this.form.get('confirm_password')) {
      if (this.form.get('password').value) {
        if (this.form.get('password').value !== this.form.get('confirm_password').value) {
          return { NotEqual: true };
        }
      } else {
        return { Required: true };
      }
    }

    return null;
  }

  private mustHaveSeed() {
    if (!this.useNormalSeed) {
      if ((this.form.get('seed').value as string) === '') {
        return { Required: true };
      } else {
        return null;
      }
    }
  }

  private seedMatchValidator() {
    if (this.useNormalSeed) {
      return null;
    }

    if (this.form && this.form.get('seed') && this.form.get('confirm_seed')) {
      return this.form.get('seed').value === this.form.get('confirm_seed').value ? null : { NotEqual: true };
    } else {
      this.customSeedIsNormal = true;

      return { NotEqual: true };
    }
  }
}
