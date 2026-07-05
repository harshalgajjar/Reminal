class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.4.1"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.4.1/reminal_1.4.1_darwin_arm64.tar.gz"
      sha256 "dbc2cce9c12a05e1f701e50fac537ceb44a0e6856c32e679b03970ed82aaf879"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.4.1/reminal_1.4.1_darwin_amd64.tar.gz"
      sha256 "d81c792d9701ce92fb05f757adcaf2a142baac6a7c8dff39d1d980b0ed700f83"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.4.1/reminal_1.4.1_linux_arm64.tar.gz"
      sha256 "04ff17d4069c44570b6188cbac6b65b0e97575bb87be1f26329d6b038287c351"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.4.1/reminal_1.4.1_linux_amd64.tar.gz"
      sha256 "f91dade2be86915f2057cd862609af4734e071c1398073dd1623287340a63764"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
